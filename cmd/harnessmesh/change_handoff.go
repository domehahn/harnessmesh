package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/workspace"
)

type handoffEvidence struct {
	ID           string `json:"id"`
	ObligationID string `json:"obligation_id"`
	Tree         string `json:"tree_hash"`
	Source       string `json:"source"`
	Adapter      string `json:"adapter,omitempty"`
	Artifact     string `json:"artifact_hash,omitempty"`
	Command      string `json:"command,omitempty"`
	Result       string `json:"result"`
	Current      bool   `json:"current"`
}

func changeHandoff(args []string) error {
	fs := flag.NewFlagSet("change handoff", flag.ContinueOnError)
	id := fs.String("change", "", "change transaction ID")
	digest := fs.String("plan-digest", "", "approved plan digest")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" || fs.NArg() != 0 {
		return errors.New("usage: harnessmesh change handoff --change <id> [--plan-digest <digest>]")
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
	tree, err := workspace.ComputeTreeHash(ctx, ".")
	if err != nil {
		return err
	}
	evidence, err := eng.GetChangeEvidence(ctx, *id)
	if err != nil {
		return err
	}
	obligations, err := eng.GetProofObligations(ctx, *id)
	if err != nil {
		return err
	}
	gate, err := eng.GetLatestGateResult(ctx, *id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	receipts := make([]handoffEvidence, 0, len(evidence))
	for _, ev := range evidence {
		receipts = append(receipts, handoffEvidence{
			ID: ev.ID, ObligationID: ev.ObligationID, Tree: ev.TreeHash, Source: ev.SourceParticipant,
			Adapter: ev.SourceAdapter, Artifact: ev.ArtifactHash, Command: ev.Command, Result: ev.Result,
			Current: ev.Valid && ev.TreeHash == tree,
		})
	}
	gateCurrent := gate != nil && gate.CurrentTreeHash == tree
	open := make([]string, 0)
	if gateCurrent {
		open = append(open, gate.PendingObligations...)
		open = append(open, gate.FailedObligations...)
		open = append(open, gate.StaleObligations...)
	} else {
		for _, obligation := range obligations {
			if obligation.Required {
				open = append(open, obligation.ID)
			}
		}
	}
	result := map[string]any{
		"change": change, "worktree_hash": tree, "obligations": obligations,
		"evidence": receipts, "last_recorded_gate": gate,
		"gate_current": gateCurrent, "open_obligation_ids": open,
	}
	if *digest != "" {
		approval, err := st.GetApproval(ctx, "change_plan_"+*digest)
		if err != nil || approval == nil || approval.Status != protocol.ApprovalApproved {
			return errors.New("approved plan not found")
		}
		var plan planDecision
		if err := json.Unmarshal([]byte(approval.Reason), &plan); err != nil || plan.Digest != *digest || plan.ChangeID != *id {
			return errors.New("approved plan does not belong to this change")
		}
		result["approved_plan"], result["approved_by"], result["approved_at"] = plan, approval.DecidedBy, approval.DecidedAt
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}
