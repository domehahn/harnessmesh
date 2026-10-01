package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// This file implements safe, opt-in diagnostic instrumentation for the
// shape of requests HarnessMesh actually receives before forwarding them
// to the SIWC route - added specifically so real Codex VS Code extension
// request shapes can be captured without guessing, and without ever
// logging anything sensitive.
//
// It logs ONLY: top-level JSON key NAMES, input item TYPE names, tool TYPE
// names, and the stream/store booleans (plus, on rejection, a compatibility
// error's own message - which by construction names only a field/type/
// capability, never a value). It NEVER logs: the OAuth bearer token, tool
// arguments, prompts, file contents, user text, or encrypted reasoning
// content. It is entirely inert unless HARNESSMESH_SIWC_DEBUG=1 is set (or
// a sink is injected directly, as tests do).

// RequestShapeDiagnostic is a fully-redacted record of one incoming
// request's shape, captured at the SIWC normalization boundary.
type RequestShapeDiagnostic struct {
	Time            time.Time
	TopLevelKeys    []string
	InputItemTypes  []string
	ToolTypes       []string
	Stream          bool
	Store           bool
	Accepted        bool
	RejectionReason string // a compatibility error's Error() text only - never a raw body
}

// RequestShapeDiagnosticsSink receives RequestShapeDiagnostic records.
type RequestShapeDiagnosticsSink interface {
	RecordRequestShape(RequestShapeDiagnostic)
}

// stderrRequestShapeDiagnosticsLogger is the only production
// RequestShapeDiagnosticsSink implementation: one redacted line to stderr
// per request reaching the SIWC normalization boundary.
type stderrRequestShapeDiagnosticsLogger struct{}

func (stderrRequestShapeDiagnosticsLogger) RecordRequestShape(d RequestShapeDiagnostic) {
	fmt.Fprintf(os.Stderr,
		"[siwc-request-diag] %s accepted=%v stream=%v store=%v keys=%v input_item_types=%v tool_types=%v rejection=%q\n",
		d.Time.Format(time.RFC3339), d.Accepted, d.Stream, d.Store, d.TopLevelKeys, d.InputItemTypes, d.ToolTypes, d.RejectionReason)
}

// requestShapeDiagnosticsSinkFromEnv returns a RequestShapeDiagnosticsSink
// only when explicitly opted into via HARNESSMESH_SIWC_DEBUG=1 - a normal
// request through the provider gateway emits nothing extra by default.
func requestShapeDiagnosticsSinkFromEnv() RequestShapeDiagnosticsSink {
	if os.Getenv("HARNESSMESH_SIWC_DEBUG") == "1" {
		return stderrRequestShapeDiagnosticsLogger{}
	}
	return nil
}

// buildRequestShapeDiagnostic derives a redacted shape record from req and
// the normalization outcome (normalized, err from normalizeForSIWC).
func buildRequestShapeDiagnostic(req Request, normalized *siwcNormalizedRequest, err error) RequestShapeDiagnostic {
	keys := make([]string, 0, len(req.RawKeys))
	for k := range req.RawKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	itemTypes := make([]string, 0, len(req.Input))
	for _, it := range req.Input {
		itemTypes = append(itemTypes, it.Type)
	}

	toolTypeSet := make(map[string]bool)
	for _, t := range req.Tools {
		toolTypeSet[t.Type] = true
	}
	for _, it := range req.Input {
		if it.Type != "additional_tools" {
			continue
		}
		for _, rawTool := range it.Tools {
			var probe struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(rawTool, &probe) == nil && probe.Type != "" {
				toolTypeSet[probe.Type] = true
			}
		}
	}
	toolTypes := make([]string, 0, len(toolTypeSet))
	for t := range toolTypeSet {
		toolTypes = append(toolTypes, t)
	}
	sort.Strings(toolTypes)

	d := RequestShapeDiagnostic{
		Time:           time.Now(),
		TopLevelKeys:   keys,
		InputItemTypes: itemTypes,
		ToolTypes:      toolTypes,
		Accepted:       err == nil,
	}
	if normalized != nil {
		d.Stream, d.Store = normalized.Stream, normalized.Store
	}
	if err != nil {
		d.RejectionReason = err.Error()
	}
	return d
}
