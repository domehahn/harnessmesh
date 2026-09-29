package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/protocol"
	"github.com/domehahn/harnessmesh/internal/store"
	"github.com/domehahn/harnessmesh/internal/workspace"
)

func changeCmd(args []string) error {
	if len(args) == 0 {
		changeUsage()
		return errors.New("subcommand required: create, list, show, prepare, verify, evidence, abort, gate, commit")
	}

	switch args[0] {
	case "create":
		return changeCreate(args[1:])
	case "list":
		return changeList(args[1:])
	case "show":
		return changeShow(args[1:])
	case "prepare":
		return changePrepare(args[1:])
	case "verify":
		return changeVerify(args[1:])
	case "evidence":
		return changeEvidence(args[1:])
	case "abort":
		return changeAbort(args[1:])
	case "gate":
		return changeGate(args[1:])
	case "commit":
		return changeCommit(args[1:])
	case "help", "-h", "--help":
		changeUsage()
		return nil
	default:
		changeUsage()
		return fmt.Errorf("unknown change subcommand %q", args[0])
	}
}

func changeUsage() {
	fmt.Println(`Usage: harnessmesh change <subcommand> [flags]

Subcommands:
  create    Propose a new change transaction and lock proof policy
  list      List change transactions
  show      Inspect change details, obligations, and gate status
  prepare   Transition change to prepared state, updating working tree
  verify    Execute automated verification proofs
  evidence  Submit evidence for a proof obligation
  gate      Evaluate the evidence gate for a change
  commit    Commit verified change to repository (TOCTOU verified)
  abort     Abort an in-flight change transaction`)
}

func getChangeEngine(configPath string) (*collaboration.Engine, store.Store, error) {
	st, err := store.OpenSQLite("")
	if err != nil {
		return nil, nil, err
	}

	cfg := &config.Config{
		ChangeControl: config.ChangeControlConfig{
			Enabled: true,
		},
	}
	if configPath != "" {
		loaded, err := config.Load(configPath)
		if err == nil && loaded != nil {
			cfg = loaded
		}
	}

	cwd, _ := os.Getwd()
	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config: cfg,
		Store:  st,
		Repo:   cwd,
	})

	return eng, st, nil
}

func changeCreate(args []string) error {
	fs := flag.NewFlagSet("change create", flag.ContinueOnError)
	title := fs.String("title", "", "change title")
	intent := fs.String("intent", "", "high-level intent")
	space := fs.String("space", "", "collaboration space ID")
	branch := fs.String("branch", "", "target git branch")
	author := fs.String("author", "operator", "author participant ID")
	baseCommit := fs.String("base", "", "base git commit")
	cfgPath := fs.String("config", "harnessmesh.json", "config file path")
	jsonOut := fs.Bool("json", false, "output JSON")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *title == "" {
		return errors.New("--title is required")
	}

	eng, st, err := getChangeEngine(*cfgPath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	req := &protocol.CreateChangeRequest{
		SpaceID:           *space,
		AuthorParticipant: *author,
		Title:             *title,
		Intent:            *intent,
		BaseCommit:        *baseCommit,
	}

	chg, err := eng.CreateChange(ctx, req)
	if err != nil {
		return err
	}

	obls, _ := eng.GetProofObligations(ctx, chg.ID)

	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"change":      chg,
			"obligations": obls,
		})
	}

	fmt.Printf("Created change transaction: %s\n", chg.ID)
	fmt.Printf("Title:        %s\n", chg.Title)
	fmt.Printf("Status:       %s\n", chg.Status)
	fmt.Printf("Tree Hash:    %s\n", chg.CurrentTreeHash)
	fmt.Printf("Author:       %s\n", chg.AuthorParticipant)
	if *branch != "" {
		fmt.Printf("Branch:       %s\n", *branch)
	}
	fmt.Printf("Locked Proof Obligations (%d):\n", len(obls))
	for _, o := range obls {
		reqStr := "optional"
		if o.Required {
			reqStr = "required"
		}
		fmt.Printf("  - [%s] %-20s (%s): %s\n", reqStr, o.Name, o.Type, o.Description)
	}

	return nil
}

func changeList(args []string) error {
	fs := flag.NewFlagSet("change list", flag.ContinueOnError)
	space := fs.String("space", "", "filter by space ID")
	status := fs.String("status", "", "filter by status (draft, prepared, under_verification, blocked, verified, committable, committed, aborted)")
	jsonOut := fs.Bool("json", false, "output JSON")

	if err := fs.Parse(args); err != nil {
		return err
	}

	eng, st, err := getChangeEngine("")
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	changes, err := eng.ListMeshChanges(ctx, *space, protocol.MeshChangeStatus(*status))
	if err != nil {
		return err
	}

	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(changes)
	}

	if len(changes) == 0 {
		fmt.Println("No change transactions found.")
		return nil
	}

	fmt.Printf("%-24s %-16s %-14s %-12s %s\n", "CHANGE ID", "STATUS", "AUTHOR", "TREE", "TITLE")
	for _, c := range changes {
		treeShort := c.CurrentTreeHash
		if len(treeShort) > 8 {
			treeShort = treeShort[:8]
		}
		fmt.Printf("%-24s %-16s %-14s %-12s %s\n", c.ID, c.Status, c.AuthorParticipant, treeShort, c.Title)
	}

	return nil
}

func changeShow(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: harnessmesh change show <change_id> [--json]")
	}
	changeID := args[0]
	jsonOut := false
	if len(args) > 1 && args[1] == "--json" {
		jsonOut = true
	}

	eng, st, err := getChangeEngine("")
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	chg, err := eng.GetMeshChange(ctx, changeID)
	if err != nil || chg == nil {
		return fmt.Errorf("change %q not found", changeID)
	}

	obls, _ := eng.GetProofObligations(ctx, changeID)
	evs, _ := eng.GetChangeEvidence(ctx, changeID)
	gate, _ := eng.GetLatestGateResult(ctx, changeID)
	paths, _ := eng.GetChangePaths(ctx, changeID)

	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"change":         chg,
			"obligations":    obls,
			"evidence":       evs,
			"gate_result":    gate,
			"affected_paths": paths,
		})
	}

	fmt.Printf("Change ID:           %s\n", chg.ID)
	fmt.Printf("Title:               %s\n", chg.Title)
	fmt.Printf("Intent:              %s\n", chg.Intent)
	fmt.Printf("Status:              %s\n", chg.Status)
	fmt.Printf("Author:              %s\n", chg.AuthorParticipant)
	fmt.Printf("Current Tree Hash:   %s\n", chg.CurrentTreeHash)
	if chg.VerifiedTreeHash != "" {
		fmt.Printf("Verified Tree Hash:  %s\n", chg.VerifiedTreeHash)
	}
	if chg.CommitSHA != "" {
		fmt.Printf("Commit SHA:          %s\n", chg.CommitSHA)
	}
	fmt.Printf("Created At:          %s\n", chg.CreatedAt.Format("2006-01-02 15:04:05 UTC"))

	if gate != nil {
		fmt.Printf("\nGate Status:         %s (evaluated %s)\n", gate.Status, gate.EvaluatedAt.Format("15:04:05 UTC"))
		if len(gate.Reasons) > 0 {
			fmt.Printf("Reasons:             %s\n", strings.Join(gate.Reasons, "; "))
		}
	}

	fmt.Printf("\nProof Obligations (%d):\n", len(obls))
	for _, o := range obls {
		req := "optional"
		if o.Required {
			req = "required"
		}
		fmt.Printf("  - [%s] %-12s %-20s (policy: %s, exit: %d)\n", req, o.Status, o.Name, o.PolicySource, o.ExpectedExitCode)
		if o.Command != "" {
			fmt.Printf("      command: %s\n", o.Command)
		}
	}

	if len(paths) > 0 {
		fmt.Printf("\nAffected Paths (%d):\n", len(paths))
		for _, p := range paths {
			fmt.Printf("  - %-8s %s\n", p.ChangeType, p.Path)
		}
	}

	if len(evs) > 0 {
		fmt.Printf("\nSubmitted Evidence (%d):\n", len(evs))
		for _, e := range evs {
			valStr := "valid"
			if !e.Valid {
				valStr = "stale"
			}
			fmt.Printf("  - [%s] %-8s %-16s by %s (tree: %s)\n", valStr, e.Result, e.EvidenceType, e.SourceParticipant, e.TreeHash[:min(8, len(e.TreeHash))])
		}
	}

	return nil
}

func changePrepare(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: harnessmesh change prepare <change_id>")
	}
	changeID := args[0]

	eng, st, err := getChangeEngine("")
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	chg, err := eng.PrepareChange(ctx, changeID, "")
	if err != nil {
		return err
	}

	fmt.Printf("Change %s transitioned to %s\n", chg.ID, chg.Status)
	fmt.Printf("Tree Hash: %s\n", chg.CurrentTreeHash)
	return nil
}

func changeVerify(args []string) error {
	fs := flag.NewFlagSet("change verify", flag.ContinueOnError)
	oblID := fs.String("obligation", "", "specific obligation ID to verify")
	jsonOut := fs.Bool("json", false, "output JSON")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if len(fs.Args()) == 0 {
		return errors.New("usage: harnessmesh change verify <change_id> [--obligation <id>]")
	}
	changeID := fs.Args()[0]

	eng, st, err := getChangeEngine("")
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()

	if *oblID != "" {
		ev, err := eng.ExecuteProof(ctx, changeID, *oblID)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(ev)
		}
		fmt.Printf("Proof %s: %s (exit code %v)\n", *oblID, ev.Result, ev.ExitCode)
		return nil
	}

	// Verify all automated proof obligations
	obls, err := eng.GetProofObligations(ctx, changeID)
	if err != nil {
		return err
	}

	executed := 0
	for _, o := range obls {
		if o.Command != "" {
			fmt.Printf("Executing proof for %s (%s)...\n", o.Name, o.ID)
			ev, err := eng.ExecuteProof(ctx, changeID, o.ID)
			if err != nil {
				fmt.Printf("  -> execution error: %v\n", err)
			} else {
				fmt.Printf("  -> %s\n", ev.Result)
				executed++
			}
		}
	}

	gate, err := eng.EvaluateGate(ctx, changeID)
	if err != nil {
		return err
	}
	fmt.Printf("\nGate Status: %s (passed: %d, pending: %d, failed: %d)\n",
		gate.Status, len(gate.PassedObligations), len(gate.PendingObligations), len(gate.FailedObligations))

	return nil
}

func changeEvidence(args []string) error {
	fs := flag.NewFlagSet("change evidence", flag.ContinueOnError)
	oblID := fs.String("obligation", "", "obligation ID")
	evType := fs.String("type", "peer_review", "evidence type (e.g. peer_review, unit_tests, security_audit)")
	result := fs.String("result", "passed", "result: passed or failed")
	source := fs.String("source", "operator", "source participant ID")
	notes := fs.String("notes", "", "evidence notes / review findings")
	jsonOut := fs.Bool("json", false, "output JSON")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if len(fs.Args()) == 0 {
		return errors.New("usage: harnessmesh change evidence <change_id> --obligation <id> --type <type> --result <passed|failed>")
	}
	changeID := fs.Args()[0]

	eng, st, err := getChangeEngine("")
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	chg, err := eng.GetMeshChange(ctx, changeID)
	if err != nil || chg == nil {
		return fmt.Errorf("change %q not found", changeID)
	}

	treeHash, err := workspace.ComputeTreeHash(ctx, ".")
	if err != nil {
		return fmt.Errorf("failed to compute tree hash: %w", err)
	}

	ev := &protocol.ChangeEvidence{
		ChangeID:          changeID,
		ObligationID:      *oblID,
		TreeHash:          treeHash,
		SourceParticipant: *source,
		EvidenceType:      protocol.EvidenceType(*evType),
		Result:            *result,
		Valid:             true,
		Metadata: map[string]any{
			"notes": *notes,
		},
	}

	gate, err := eng.SubmitChangeEvidence(ctx, ev)
	if err != nil {
		return err
	}

	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(gate)
	}

	fmt.Printf("Submitted %s evidence for obligation %q\n", *result, *oblID)
	fmt.Printf("Updated Gate Status: %s\n", gate.Status)
	return nil
}

func changeGate(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: harnessmesh change gate <change_id> [--json]")
	}
	changeID := args[0]
	jsonOut := false
	if len(args) > 1 && args[1] == "--json" {
		jsonOut = true
	}

	eng, st, err := getChangeEngine("")
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	gate, err := eng.EvaluateGate(ctx, changeID)
	if err != nil {
		return err
	}

	if jsonOut {
		return json.NewEncoder(os.Stdout).Encode(gate)
	}

	fmt.Printf("Gate Status:         %s\n", gate.Status)
	fmt.Printf("Verified Tree Hash:  %s\n", gate.VerifiedTreeHash)
	fmt.Printf("Current Tree Hash:   %s\n", gate.CurrentTreeHash)
	fmt.Printf("Passed Obligations:  %d (%s)\n", len(gate.PassedObligations), strings.Join(gate.PassedObligations, ", "))
	fmt.Printf("Pending Obligations: %d (%s)\n", len(gate.PendingObligations), strings.Join(gate.PendingObligations, ", "))
	fmt.Printf("Failed Obligations:  %d (%s)\n", len(gate.FailedObligations), strings.Join(gate.FailedObligations, ", "))
	if len(gate.Reasons) > 0 {
		fmt.Printf("Reasons:\n")
		for _, r := range gate.Reasons {
			fmt.Printf("  - %s\n", r)
		}
	}

	return nil
}

func changeCommit(args []string) error {
	fs := flag.NewFlagSet("change commit", flag.ContinueOnError)
	msg := fs.String("message", "", "commit message")
	author := fs.String("author", "operator", "author participant ID")
	jsonOut := fs.Bool("json", false, "output JSON")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if len(fs.Args()) == 0 {
		return errors.New("usage: harnessmesh change commit <change_id> [--message <msg>]")
	}
	changeID := fs.Args()[0]

	eng, st, err := getChangeEngine("")
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	chg, err := eng.CommitChange(ctx, changeID, *author, *msg)
	if err != nil {
		return err
	}

	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(chg)
	}

	fmt.Printf("Successfully committed change %s!\n", chg.ID)
	fmt.Printf("Commit Hash: %s\n", chg.CommitSHA)
	fmt.Printf("Tree Hash:   %s\n", chg.CurrentTreeHash)
	if chg.CommittedAt != nil {
		fmt.Printf("Time:        %s\n", chg.CommittedAt.Format("2006-01-02 15:04:05 UTC"))
	}
	return nil
}

func changeAbort(args []string) error {
	fs := flag.NewFlagSet("change abort", flag.ContinueOnError)
	reason := fs.String("reason", "aborted by operator", "abort reason")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if len(fs.Args()) == 0 {
		return errors.New("usage: harnessmesh change abort <change_id> [--reason <reason>]")
	}
	changeID := fs.Args()[0]

	eng, st, err := getChangeEngine("")
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	chg, err := eng.AbortChange(ctx, changeID, "", *reason)
	if err != nil {
		return err
	}

	fmt.Printf("Change %s aborted (%s)\n", chg.ID, *reason)
	return nil
}
