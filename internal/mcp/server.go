package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/protocol"
)

type Server struct {
	engine    *collaboration.Engine
	sessionID string
	caller    string
	mu        sync.Mutex
}

func NewServer(engine *collaboration.Engine, sessionID, caller string) *Server {
	if caller == "" {
		caller = "participant"
	}
	return &Server{
		engine:    engine,
		sessionID: sessionID,
		caller:    caller,
	}
}

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id"`
	Result  any           `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type ToolCallContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolCallResult struct {
	Content []ToolCallContent `json:"content"`
	IsError bool              `json:"isError,omitempty"`
}

func (s *Server) ListTools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "peer.list",
			Description: "List all known peer agents, their adapter types, roles, and status in the session.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"session_id": map[string]any{
						"type":        "string",
						"description": "Optional session ID",
					},
				},
			},
		},
		{
			Name:        "peer.capabilities",
			Description: "Discover peer agents matching a required capability or role.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"capability": map[string]any{
						"type":        "string",
						"description": "Required capability or role (e.g. 'security_review', 'performance_review', 'review', 'answer_questions')",
					},
				},
				"required": []string{"capability"},
			},
		},
		{
			Name:        "peer.ask",
			Description: "Ask a peer agent for targeted help or code inspection while working.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"peer": map[string]any{
						"type":        "string",
						"description": "Name of the target peer agent (e.g. 'codex', 'antigravity')",
					},
					"capability": map[string]any{
						"type":        "string",
						"description": "Optional capability to route the question to if peer is omitted",
					},
					"question": map[string]any{
						"type":        "string",
						"description": "The specific question or inspection request",
					},
					"scope": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Repository file paths or directories to inspect",
					},
					"context": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"include_diff":       map[string]any{"type": "boolean"},
							"include_tests":      map[string]any{"type": "boolean"},
							"include_git_status": map[string]any{"type": "boolean"},
						},
					},
					"idempotency_key": map[string]any{
						"type":        "string",
						"description": "Optional idempotency key to prevent duplicate expensive executions",
					},
				},
				"required": []string{"question"},
			},
		},
		{
			Name:        "peer.request_review",
			Description: "Request a structured review of work-in-progress or completed changes from one or more peer reviewers.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"peer": map[string]any{
						"type":        "string",
						"description": "Name of the target peer reviewer",
					},
					"capability": map[string]any{
						"type":        "string",
						"description": "Optional capability to resolve a reviewer if peer is omitted",
					},
					"reviewers": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"peer":       map[string]any{"type": "string"},
								"capability": map[string]any{"type": "string"},
								"focus":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							},
						},
						"description": "Optional list of multiple reviewers for parallel multi-review",
					},
					"scope": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "File paths or package scopes under review",
					},
					"focus": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Review focus areas (e.g. 'correctness', 'concurrency', 'security', 'test coverage')",
					},
					"changed_files_only": map[string]any{"type": "boolean"},
					"include_diff":       map[string]any{"type": "boolean"},
					"include_tests":      map[string]any{"type": "boolean"},
					"minimum_severity":   map[string]any{"type": "string"},
					"idempotency_key":    map[string]any{"type": "string"},
				},
			},
		},
		{
			Name:        "peer.submit_finding",
			Description: "Publish a structured finding into the shared collaboration session.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":             map[string]any{"type": "string"},
					"severity":       map[string]any{"type": "string", "enum": []string{"info", "low", "medium", "high", "critical"}},
					"category":       map[string]any{"type": "string"},
					"claim":          map[string]any{"type": "string"},
					"evidence":       map[string]any{"type": "string"},
					"recommendation": map[string]any{"type": "string"},
					"file":           map[string]any{"type": "string"},
					"line":           map[string]any{"type": "integer"},
					"status":         map[string]any{"type": "string", "enum": []string{"open", "acknowledged", "disputed", "resolved", "dismissed"}},
				},
				"required": []string{"severity", "claim", "evidence", "recommendation"},
			},
		},
		{
			Name:        "peer.submit_evidence",
			Description: "Submit verifiable repository evidence (test results, diffs, static analysis) linked to findings.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"finding_id": map[string]any{"type": "string"},
					"type":       map[string]any{"type": "string"},
					"command":    map[string]any{"type": "string"},
					"result":     map[string]any{"type": "string"},
					"excerpt":    map[string]any{"type": "string"},
					"exit_code":  map[string]any{"type": "integer"},
				},
				"required": []string{"type"},
			},
		},
		{
			Name:        "peer.challenge",
			Description: "Formally challenge a peer finding with counter-claims and evidence, marking it as disputed.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"finding_id":             map[string]any{"type": "string"},
					"claim":                  map[string]any{"type": "string"},
					"evidence":               map[string]any{"type": "string"},
					"requested_verification": map[string]any{"type": "string"},
				},
				"required": []string{"finding_id", "claim", "evidence"},
			},
		},
		{
			Name:        "peer.resolve",
			Description: "Resolve an open or disputed finding with evidence-driven rationale.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"finding_id":          map[string]any{"type": "string"},
					"status":              map[string]any{"type": "string", "enum": []string{"confirmed", "rejected", "partially_confirmed", "superseded", "requires_human"}},
					"rationale":           map[string]any{"type": "string"},
					"evidence_references": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"finding_id", "status", "rationale"},
			},
		},
		{
			Name:        "peer.reply",
			Description: "Provide an explicit structured reply to a parent peer message.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"parent_message_id":      map[string]any{"type": "string"},
					"target_peer":            map[string]any{"type": "string"},
					"message":                map[string]any{"type": "string"},
					"evidence_references":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"finding_references":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"expected_response_type": map[string]any{"type": "string"},
				},
				"required": []string{"parent_message_id", "target_peer", "message"},
			},
		},
		{
			Name:        "peer.converse",
			Description: "Converse interactively with a peer agent (e.g. OpenAI Codex) for a second opinion, architecture check, code review, debugging help, security analysis, or validation. Seamlessly maintains peer thread context across multiple turns.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"peer": map[string]any{
						"type":        "string",
						"description": "Name of target peer agent (e.g. 'codex', 'openai-reviewer'). If omitted, routes by capability.",
					},
					"capability": map[string]any{
						"type":        "string",
						"description": "Optional capability or role to route to if peer name is omitted (e.g. 'review', 'architecture', 'security')",
					},
					"message": map[string]any{
						"type":        "string",
						"description": "The message, review request, architectural question, or follow-up to the peer",
					},
					"scope": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Repository file paths or directories to inspect or focus on",
					},
					"context": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"include_diff":       map[string]any{"type": "boolean"},
							"include_tests":      map[string]any{"type": "boolean"},
							"include_git_status": map[string]any{"type": "boolean"},
							"files":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
						},
					},
					"expected_outcome": map[string]any{
						"type":        "string",
						"description": "Expected outcome: 'second_opinion', 'review', 'architecture_check', 'debugging_help', 'security_analysis', 'validation'",
					},
					"causation_id": map[string]any{
						"type":        "string",
						"description": "Optional message ID being responded to or followed up on",
					},
					"idempotency_key": map[string]any{
						"type":        "string",
						"description": "Optional idempotency key to prevent duplicate calls",
					},
				},
				"required": []string{"message"},
			},
		},
		{
			Name:        "peer.status",
			Description: "Retrieve current collaboration session status, findings counts, and remaining budget/rounds.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "collaboration.publish",
			Description: "Publish a message, question, finding, or proposal to a channel or thread in the persistent collaboration space.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id":  map[string]any{"type": "string", "description": "Collaboration space ID (defaults to active session)"},
					"channel":   map[string]any{"type": "string", "description": "Channel name or ID (e.g. 'general', 'architecture', 'security', 'findings', 'decisions')"},
					"thread_id": map[string]any{"type": "string", "description": "Optional thread ID to continue an existing thread"},
					"subject":   map[string]any{"type": "string", "description": "Subject or title for a new thread"},
					"message":   map[string]any{"type": "string", "description": "The message text or query"},
					"mentions":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Participants to mention and activate (e.g. ['codex', 'copilot'])"},
					"scope":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Repository files or paths relevant to this message"},
					"metadata":  map[string]any{"type": "object", "description": "Optional arbitrary metadata"},
				},
				"required": []string{"message"},
			},
		},
		{
			Name:        "collaboration.reply",
			Description: "Post a reply in an existing collaboration thread.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id":  map[string]any{"type": "string", "description": "Collaboration space ID"},
					"channel":   map[string]any{"type": "string", "description": "Channel name or ID"},
					"thread_id": map[string]any{"type": "string", "description": "Thread ID to reply to"},
					"message":   map[string]any{"type": "string", "description": "The reply message text"},
					"mentions":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional mentions"},
					"scope":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional file scope"},
				},
				"required": []string{"thread_id", "message"},
			},
		},
		{
			Name:        "collaboration.inbox",
			Description: "Retrieve messages, mentions, and notifications from the participant's inbox.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id":    map[string]any{"type": "string", "description": "Collaboration space ID"},
					"unread_only": map[string]any{"type": "boolean", "description": "Only return unread messages"},
				},
			},
		},
		{
			Name:        "collaboration.channels",
			Description: "List all channels in the collaboration space.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id": map[string]any{"type": "string", "description": "Collaboration space ID"},
				},
			},
		},
		{
			Name:        "collaboration.thread",
			Description: "Retrieve the full message transcript of a thread.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id":  map[string]any{"type": "string", "description": "Collaboration space ID"},
					"channel":   map[string]any{"type": "string", "description": "Channel name or ID"},
					"thread_id": map[string]any{"type": "string", "description": "Thread ID"},
				},
				"required": []string{"thread_id"},
			},
		},
		{
			Name:        "collaboration.subscribe",
			Description: "Subscribe to specific events or channels in the collaboration space.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id":       map[string]any{"type": "string", "description": "Collaboration space ID"},
					"channels":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Channels to subscribe to"},
					"event_types":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Event types to subscribe to (e.g. 'repository.changed', 'finding.created')"},
					"scope_patterns": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Glob patterns for file scope"},
					"mode":           map[string]any{"type": "string", "description": "Participant activity mode: 'active', 'passive', 'on_demand', 'paused'"},
				},
			},
		},
		{
			Name:        "collaboration.unsubscribe",
			Description: "Remove an event subscription.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"subscription_id": map[string]any{"type": "string", "description": "Subscription ID to delete"},
				},
				"required": []string{"subscription_id"},
			},
		},
		{
			Name:        "collaboration.decide",
			Description: "Propose or accept an evidence-backed architectural or engineering decision.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id":            map[string]any{"type": "string", "description": "Collaboration space ID"},
					"action":              map[string]any{"type": "string", "description": "Action: 'propose' or 'accept'"},
					"decision_id":         map[string]any{"type": "string", "description": "Decision ID (required for 'accept')"},
					"title":               map[string]any{"type": "string", "description": "Title of the decision (required for 'propose')"},
					"statement":           map[string]any{"type": "string", "description": "Clear statement of the decision (required for 'propose')"},
					"rationale":           map[string]any{"type": "string", "description": "Technical rationale (required for 'propose')"},
					"evidence_references": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Evidence IDs supporting the decision"},
				},
				"required": []string{"action"},
			},
		},
		{
			Name:        "collaboration.status",
			Description: "Retrieve comprehensive collaboration space status, participants, channels, and decisions.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"space_id": map[string]any{"type": "string", "description": "Collaboration space ID"},
				},
			},
		},
	}
}

func (s *Server) HandleMessage(ctx context.Context, raw []byte) (*JSONRPCResponse, error) {
	var req JSONRPCRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			Error:   &JSONRPCError{Code: -32700, Message: "Parse error"},
		}, nil
	}

	switch req.Method {
	case "initialize":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "harnessmesh",
					"version": "0.3.0",
				},
			},
		}, nil

	case "notifications/initialized":
		return nil, nil // No response for notifications

	case "ping":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}, nil

	case "tools/list":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": s.ListTools(),
			},
		}, nil

	case "tools/call":
		var callParams struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &callParams); err != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &JSONRPCError{Code: -32602, Message: "Invalid params"},
			}, nil
		}

		res, err := s.executeTool(ctx, callParams.Name, callParams.Arguments)
		if err != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: ToolCallResult{
					Content: []ToolCallContent{
						{Type: "text", Text: fmt.Sprintf("Error: %v", err)},
					},
					IsError: true,
				},
			}, nil
		}

		resJSON, _ := json.MarshalIndent(res, "", "  ")
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: ToolCallResult{
				Content: []ToolCallContent{
					{Type: "text", Text: string(resJSON)},
				},
			},
		}, nil

	default:
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &JSONRPCError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)},
		}, nil
	}
}

func (s *Server) executeTool(ctx context.Context, toolName string, argsJSON json.RawMessage) (any, error) {
	if s.sessionID == "" {
		// Auto-create or resolve active session if not provided
		sess, err := s.engine.CreateSession(ctx, "", "MCP live collaboration")
		if err != nil {
			return nil, err
		}
		s.sessionID = sess.ID
	}

	switch toolName {
	case "peer.list":
		var listReq struct {
			SessionID string `json:"session_id"`
		}
		if len(argsJSON) > 0 {
			_ = json.Unmarshal(argsJSON, &listReq)
		}
		sessID := listReq.SessionID
		if sessID == "" {
			sessID = s.sessionID
		}
		return s.engine.ListParticipants(ctx, sessID)

	case "peer.capabilities":
		var capReq protocol.PeerCapabilitiesRequest
		if err := json.Unmarshal(argsJSON, &capReq); err != nil {
			return nil, fmt.Errorf("parse peer.capabilities args: %w", err)
		}
		return s.engine.DiscoverCapabilities(ctx, capReq)

	case "peer.ask":
		var askReq struct {
			Peer           string                     `json:"peer"`
			Capability     string                     `json:"capability"`
			Question       string                     `json:"question"`
			Scope          []string                   `json:"scope"`
			Context        protocol.AskContextOptions `json:"context"`
			IdempotencyKey string                     `json:"idempotency_key"`
		}
		if err := json.Unmarshal(argsJSON, &askReq); err != nil {
			return nil, fmt.Errorf("parse peer.ask args: %w", err)
		}
		return s.engine.Ask(ctx, s.sessionID, s.caller, protocol.AskRequest{
			Peer:       askReq.Peer,
			Capability: askReq.Capability,
			Question:   askReq.Question,
			Scope:      askReq.Scope,
			Context:    askReq.Context,
		}, 1, askReq.IdempotencyKey)

	case "peer.request_review":
		var revReq struct {
			Peer             string                  `json:"peer"`
			Capability       string                  `json:"capability"`
			Reviewers        []protocol.ReviewerSpec `json:"reviewers"`
			Scope            []string                `json:"scope"`
			Focus            []string                `json:"focus"`
			ChangedFilesOnly bool                    `json:"changed_files_only"`
			IncludeDiff      bool                    `json:"include_diff"`
			IncludeTests     bool                    `json:"include_tests"`
			MinimumSeverity  string                  `json:"minimum_severity"`
			IdempotencyKey   string                  `json:"idempotency_key"`
		}
		if err := json.Unmarshal(argsJSON, &revReq); err != nil {
			return nil, fmt.Errorf("parse peer.request_review args: %w", err)
		}
		return s.engine.RequestReview(ctx, s.sessionID, s.caller, protocol.ReviewRequestPayload{
			Peer:             revReq.Peer,
			Capability:       revReq.Capability,
			Reviewers:        revReq.Reviewers,
			Scope:            revReq.Scope,
			Focus:            revReq.Focus,
			ChangedFilesOnly: revReq.ChangedFilesOnly,
			IncludeDiff:      revReq.IncludeDiff,
			IncludeTests:     revReq.IncludeTests,
			MinimumSeverity:  revReq.MinimumSeverity,
		}, 1, revReq.IdempotencyKey)

	case "peer.submit_finding":
		var fp protocol.FindingPayload
		if err := json.Unmarshal(argsJSON, &fp); err != nil {
			return nil, fmt.Errorf("parse peer.submit_finding args: %w", err)
		}
		return s.engine.SubmitFinding(ctx, s.sessionID, s.caller, fp)

	case "peer.submit_evidence":
		var ev protocol.EvidencePayload
		if err := json.Unmarshal(argsJSON, &ev); err != nil {
			return nil, fmt.Errorf("parse peer.submit_evidence args: %w", err)
		}
		return s.engine.SubmitEvidence(ctx, s.sessionID, s.caller, ev)

	case "peer.challenge":
		var ch protocol.ChallengePayload
		if err := json.Unmarshal(argsJSON, &ch); err != nil {
			return nil, fmt.Errorf("parse peer.challenge args: %w", err)
		}
		return s.engine.Challenge(ctx, s.sessionID, s.caller, ch)

	case "peer.resolve":
		var res protocol.ResolutionPayload
		if err := json.Unmarshal(argsJSON, &res); err != nil {
			return nil, fmt.Errorf("parse peer.resolve args: %w", err)
		}
		return s.engine.Resolve(ctx, s.sessionID, s.caller, res)

	case "peer.reply":
		var rep protocol.ReplyPayload
		if err := json.Unmarshal(argsJSON, &rep); err != nil {
			return nil, fmt.Errorf("parse peer.reply args: %w", err)
		}
		return s.engine.Reply(ctx, s.sessionID, s.caller, rep, 1, "")

	case "peer.converse":
		var convReq struct {
			Peer                 string                          `json:"peer"`
			Capability           string                          `json:"capability"`
			Message              string                          `json:"message"`
			Scope                []string                        `json:"scope"`
			Context              protocol.ConverseContextOptions `json:"context"`
			ExpectedResponseType string                          `json:"expected_outcome"`
			CausationID          string                          `json:"causation_id"`
			IdempotencyKey       string                          `json:"idempotency_key"`
		}
		if err := json.Unmarshal(argsJSON, &convReq); err != nil {
			return nil, fmt.Errorf("parse peer.converse args: %w", err)
		}
		return s.engine.Converse(ctx, s.sessionID, s.caller, protocol.ConverseRequest{
			Peer:                 convReq.Peer,
			Capability:           convReq.Capability,
			Message:              convReq.Message,
			Scope:                convReq.Scope,
			Context:              convReq.Context,
			ExpectedResponseType: convReq.ExpectedResponseType,
			CausationID:          convReq.CausationID,
		}, 1, convReq.IdempotencyKey)

	case "peer.status":
		return s.engine.Status(ctx, s.sessionID)

	case "collaboration.publish":
		var pubReq protocol.PublishRequest
		if err := json.Unmarshal(argsJSON, &pubReq); err != nil {
			return nil, fmt.Errorf("parse collaboration.publish args: %w", err)
		}
		if pubReq.SpaceID == "" {
			pubReq.SpaceID = s.sessionID
		}
		if pubReq.From == "" {
			pubReq.From = s.caller
		}
		return s.engine.Publish(ctx, &pubReq)

	case "collaboration.reply":
		var repReq struct {
			SpaceID  string   `json:"space_id"`
			Channel  string   `json:"channel"`
			ThreadID string   `json:"thread_id"`
			Message  string   `json:"message"`
			Mentions []string `json:"mentions"`
			Scope    []string `json:"scope"`
		}
		if err := json.Unmarshal(argsJSON, &repReq); err != nil {
			return nil, fmt.Errorf("parse collaboration.reply args: %w", err)
		}
		spaceID := repReq.SpaceID
		if spaceID == "" {
			spaceID = s.sessionID
		}
		return s.engine.PublishReply(ctx, spaceID, repReq.Channel, repReq.ThreadID, s.caller, repReq.Message, repReq.Mentions, repReq.Scope)

	case "collaboration.inbox":
		var inReq struct {
			SpaceID    string `json:"space_id"`
			UnreadOnly bool   `json:"unread_only"`
		}
		if len(argsJSON) > 0 {
			_ = json.Unmarshal(argsJSON, &inReq)
		}
		spaceID := inReq.SpaceID
		if spaceID == "" {
			spaceID = s.sessionID
		}
		return s.engine.GetInbox(ctx, spaceID, s.caller, nil, inReq.UnreadOnly)

	case "collaboration.channels":
		var chReq struct {
			SpaceID string `json:"space_id"`
		}
		if len(argsJSON) > 0 {
			_ = json.Unmarshal(argsJSON, &chReq)
		}
		spaceID := chReq.SpaceID
		if spaceID == "" {
			spaceID = s.sessionID
		}
		return s.engine.SpaceService().ListChannels(ctx, spaceID)

	case "collaboration.thread":
		var thReq struct {
			SpaceID  string `json:"space_id"`
			Channel  string `json:"channel"`
			ThreadID string `json:"thread_id"`
		}
		if err := json.Unmarshal(argsJSON, &thReq); err != nil {
			return nil, fmt.Errorf("parse collaboration.thread args: %w", err)
		}
		spaceID := thReq.SpaceID
		if spaceID == "" {
			spaceID = s.sessionID
		}
		return s.engine.Store().GetSpaceMessages(ctx, spaceID, thReq.Channel, thReq.ThreadID, 100)

	case "collaboration.subscribe":
		var sub protocol.Subscription
		if err := json.Unmarshal(argsJSON, &sub); err != nil {
			return nil, fmt.Errorf("parse collaboration.subscribe args: %w", err)
		}
		if sub.SpaceID == "" {
			sub.SpaceID = s.sessionID
		}
		if sub.ParticipantID == "" {
			sub.ParticipantID = s.caller
		}
		if err := s.engine.Subscribe(ctx, &sub); err != nil {
			return nil, err
		}
		return map[string]any{"status": "subscribed", "subscription_id": sub.ID}, nil

	case "collaboration.unsubscribe":
		var unsubReq struct {
			SubscriptionID string `json:"subscription_id"`
		}
		if err := json.Unmarshal(argsJSON, &unsubReq); err != nil {
			return nil, fmt.Errorf("parse collaboration.unsubscribe args: %w", err)
		}
		if err := s.engine.Unsubscribe(ctx, unsubReq.SubscriptionID); err != nil {
			return nil, err
		}
		return map[string]any{"status": "unsubscribed", "subscription_id": unsubReq.SubscriptionID}, nil

	case "collaboration.decide":
		var decReq struct {
			SpaceID            string   `json:"space_id"`
			Action             string   `json:"action"`
			DecisionID         string   `json:"decision_id"`
			Title              string   `json:"title"`
			Statement          string   `json:"statement"`
			Rationale          string   `json:"rationale"`
			EvidenceReferences []string `json:"evidence_references"`
		}
		if err := json.Unmarshal(argsJSON, &decReq); err != nil {
			return nil, fmt.Errorf("parse collaboration.decide args: %w", err)
		}
		spaceID := decReq.SpaceID
		if spaceID == "" {
			spaceID = s.sessionID
		}
		if decReq.Action == "accept" {
			return s.engine.AcceptDecision(ctx, spaceID, decReq.DecisionID, s.caller)
		}
		return s.engine.CreateDecision(ctx, spaceID, decReq.Title, decReq.Statement, decReq.Rationale, s.caller, decReq.EvidenceReferences)

	case "collaboration.status":
		var stReq struct {
			SpaceID string `json:"space_id"`
		}
		if len(argsJSON) > 0 {
			_ = json.Unmarshal(argsJSON, &stReq)
		}
		spaceID := stReq.SpaceID
		if spaceID == "" {
			spaceID = s.sessionID
		}
		return s.engine.SpaceStatus(ctx, spaceID)

	default:
		return nil, fmt.Errorf("unknown collaboration tool %q", toolName)
	}
}

// ServeStdio starts the MCP JSON-RPC server reading lines from r and writing responses to w.
func (s *Server) ServeStdio(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		resp, err := s.HandleMessage(ctx, []byte(line))
		if err != nil {
			return err
		}
		if resp != nil {
			bytes, err := json.Marshal(resp)
			if err != nil {
				return err
			}
			s.mu.Lock()
			_, _ = w.Write(append(bytes, '\n'))
			s.mu.Unlock()
		}
	}
	return scanner.Err()
}
