package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/workspace"
)

type planDecision struct {
	ChangeID string                             `json:"change_id"`
	Digest   string                             `json:"digest"`
	Tree     string                             `json:"tree_hash"`
	Spec     collaboration.SpecContract         `json:"spec"`
	Checks   collaboration.SpecValidationReport `json:"validation"`
}

func changePlan(args []string) error {
	if len(args) == 0 || !slices.Contains([]string{"show", "approve", "run"}, args[0]) {
		return errors.New("usage: harnessmesh change plan <show|approve|run> --spec <file> --change <id> [options]")
	}
	action := args[0]
	fs := flag.NewFlagSet("change plan "+action, flag.ContinueOnError)
	file := fs.String("spec", "", "JSON specification file")
	id := fs.String("change", "", "change transaction ID")
	digest := fs.String("digest", "", "reviewed plan digest")
	reviewer := fs.String("reviewer", "", "independent local operator ID")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *file == "" || *id == "" || (action != "run" && fs.NArg() != 0) || (action == "run" && fs.NArg() == 0) {
		return errors.New("--spec and --change are required; run also requires -- followed by the executor command")
	}
	ctx := context.Background()
	eng, st, err := getChangeEngine("")
	if err != nil {
		return err
	}
	defer st.Close()
	change, err := eng.GetMeshChange(ctx, *id)
	if err != nil || change == nil {
		return fmt.Errorf("change %q not found", *id)
	}
	if change.Status == protocol.ChangeStatusCommitted || change.Status == protocol.ChangeStatusAborted {
		return fmt.Errorf("change %q is finalized", *id)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	dossier, err := loadPlan(ctx, cwd, *file, change)
	if err != nil {
		return err
	}
	if action == "show" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(dossier)
	}
	if len(dossier.Spec.AcceptanceCriteria) == 0 {
		return errors.New("plan requires at least one acceptance criterion")
	}
	for _, criterion := range dossier.Spec.AcceptanceCriteria {
		if strings.TrimSpace(criterion) == "" {
			return errors.New("acceptance criteria must be nonempty")
		}
	}
	if !dossier.Checks.Ready {
		return fmt.Errorf("plan failed validation: %s", strings.Join(dossier.Checks.FailedRules, ", "))
	}
	obligations, err := eng.GetProofObligations(ctx, *id)
	if err != nil {
		return err
	}
	if err := validateAcceptanceProofs(dossier.Spec, obligations); err != nil {
		return err
	}
	approvalID := "change_plan_" + dossier.Digest
	snapshot, err := json.Marshal(dossier)
	if err != nil {
		return err
	}
	if action == "approve" {
		reviewerID := strings.TrimSpace(*reviewer)
		if *digest != dossier.Digest || reviewerID == "" || reviewerID == change.AuthorParticipant {
			return errors.New("approval requires the displayed --digest and an independent --reviewer")
		}
		now := time.Now().UTC()
		return st.SaveApproval(ctx, &protocol.ApprovalRequest{
			ID: approvalID, SessionID: change.SessionID, Agent: change.AuthorParticipant,
			Reason: string(snapshot), Status: protocol.ApprovalApproved,
			RequestedBy: change.AuthorParticipant, DecidedBy: reviewerID, CreatedAt: now, DecidedAt: &now,
		})
	}
	approval, err := st.GetApproval(ctx, approvalID)
	if err != nil || approval == nil || approval.Status != protocol.ApprovalApproved ||
		approval.Agent != change.AuthorParticipant || approval.DecidedBy == "" || approval.DecidedBy == change.AuthorParticipant {
		return &protocol.ApprovalRequiredError{ApprovalID: approvalID, Agent: change.AuthorParticipant, Reason: "review the current plan revision and worktree"}
	}

	currentTree, err := workspace.ComputeTreeHash(ctx, cwd)
	if err != nil || currentTree != dossier.Tree {
		return errors.New("worktree changed after plan verification")
	}
	command := exec.CommandContext(ctx, fs.Arg(0), fs.Args()[1:]...)
	command.Dir = cwd
	command.Stdin = strings.NewReader(string(snapshot) + "\n")
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}

func loadPlan(ctx context.Context, root, file string, change *protocol.MeshChange) (planDecision, error) {
	f, err := os.Open(file)
	if err != nil {
		return planDecision{}, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var spec collaboration.SpecContract
	if err := decoder.Decode(&spec); err != nil {
		return planDecision{}, fmt.Errorf("read plan: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return planDecision{}, errors.New("plan must contain exactly one JSON document")
	}
	for i := range spec.DecisionReferences {
		ref := &spec.DecisionReferences[i]
		clean := filepath.ToSlash(filepath.Clean(ref.Path))
		if filepath.IsAbs(ref.Path) || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return planDecision{}, fmt.Errorf("invalid decision path %q", ref.Path)
		}
		revision, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--verify", "--end-of-options", ref.Revision+"^{commit}").Output()
		if err != nil {
			return planDecision{}, fmt.Errorf("resolve decision revision for %q: %w", ref.Path, err)
		}
		ref.Path, ref.Revision = clean, strings.TrimSpace(string(revision))
		kind, err := exec.CommandContext(ctx, "git", "-C", root, "cat-file", "-t", ref.Revision+":"+ref.Path).Output()
		if err != nil || strings.TrimSpace(string(kind)) != "blob" {
			return planDecision{}, fmt.Errorf("decision %q must be a file at revision %s", ref.Path, ref.Revision)
		}
	}
	tree, err := workspace.ComputeTreeHash(ctx, root)
	if err != nil {
		return planDecision{}, err
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return planDecision{}, err
	}
	payload, err := json.Marshal(struct {
		Change string
		Author string
		Root   string
		Tree   string
		Spec   collaboration.SpecContract
	}{change.ID, change.AuthorParticipant, canonicalRoot, tree, spec})
	if err != nil {
		return planDecision{}, err
	}
	return planDecision{ChangeID: change.ID, Digest: fmt.Sprintf("%x", sha256.Sum256(payload)), Tree: tree, Spec: spec, Checks: collaboration.ValidateSpec(spec)}, nil
}

func validateAcceptanceProofs(spec collaboration.SpecContract, obligations []*protocol.ProofObligation) error {
	known := make(map[string]bool, len(obligations))
	for _, obligation := range obligations {
		known[obligation.Type] = true
	}
	for criterion, types := range spec.AcceptanceProofs {
		if !slices.Contains(spec.AcceptanceCriteria, criterion) || len(types) == 0 {
			return fmt.Errorf("invalid proof mapping for acceptance criterion %q", criterion)
		}
		for _, proofType := range types {
			if !known[proofType] {
				return fmt.Errorf("unknown proof type %q for criterion %q", proofType, criterion)
			}
		}
	}
	return nil
}
