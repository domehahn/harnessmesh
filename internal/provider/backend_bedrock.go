package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brdocument "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/domehahn/harnessmesh/internal/config"
)

// BedrockBackend routes inference through AWS Bedrock's Converse/
// ConverseStream API, using the standard AWS SDK v2 credential chain
// (environment, shared config/profile, IAM role, etc. - never HarnessMesh's
// own bespoke credential handling). It is not a metered-per-OpenAI-request
// backend in the zero-credit sense (it is not the OpenAI API or Codex), so
// zero-credit mode does not forbid it by default; an operator who wants it
// forbidden anyway can add "bedrock" to denied_backend_types.
type BedrockBackend struct {
	name    string
	modelID string
	region  string

	clientOnce sync.Once
	client     *bedrockruntime.Client
	clientErr  error
}

func NewBedrockBackend(name string, cfg config.ProviderBackendConfig) *BedrockBackend {
	region := cfg.BaseURL // reused field: for this backend type, BaseURL names an AWS region, not an HTTP URL
	return &BedrockBackend{name: name, modelID: cfg.Model, region: region}
}

func (b *BedrockBackend) Name() string { return b.name }
func (b *BedrockBackend) Type() string { return "bedrock" }

func (b *BedrockBackend) Capabilities() Capabilities {
	return Capabilities{Streaming: true, Tools: true}
}

// ensureClient builds the Bedrock client exactly once, even under
// concurrent first-request traffic: AWS credential-chain resolution
// (file I/O, and potentially IMDS/STS network calls) is expensive enough
// that letting every concurrent caller race to build their own client
// would both waste that work N times and, since the unsynchronized
// assignment to b.client was a genuine data race, corrupt the shared
// field under `go test -race`.
func (b *BedrockBackend) ensureClient(ctx context.Context) error {
	b.clientOnce.Do(func() {
		var opts []func(*awsconfig.LoadOptions) error
		if b.region != "" {
			opts = append(opts, awsconfig.WithRegion(b.region))
		}
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
		if err != nil {
			b.clientErr = &BackendUnavailableError{Backend: b.name, Reason: fmt.Sprintf("load AWS config: %v", err)}
			return
		}
		b.client = bedrockruntime.NewFromConfig(awsCfg)
	})
	return b.clientErr
}

func (b *BedrockBackend) Health(ctx context.Context) error {
	if err := b.ensureClient(ctx); err != nil {
		return err
	}
	if b.modelID == "" {
		return &BackendUnavailableError{Backend: b.name, Reason: "model is not configured"}
	}
	return nil
}

func (b *BedrockBackend) StreamResponse(ctx context.Context, req Request, sink Sink) error {
	if err := b.ensureClient(ctx); err != nil {
		return err
	}
	modelID := b.modelID
	if req.Model != "" && b.modelID == "" {
		modelID = req.Model
	}
	if modelID == "" {
		return &BackendUnavailableError{Backend: b.name, Reason: "no model configured or requested"}
	}

	messages, system := translateToBedrock(req)
	toolConfig := translateToolsToBedrock(req.Tools)

	out, err := b.client.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{
		ModelId:    aws.String(modelID),
		Messages:   messages,
		System:     system,
		ToolConfig: toolConfig,
	})
	if err != nil {
		return &BackendUnavailableError{Backend: b.name, Reason: err.Error()}
	}

	responseID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	now := time.Now().Unix()
	if err := sink.Send(StreamEvent{Type: "response.created", Response: &Response{ID: responseID, Object: "response", CreatedAt: now, Status: StatusInProgress, Model: modelID, Output: []OutputItem{}}}); err != nil {
		return err
	}

	msgItemID := fmt.Sprintf("%s_msg_0", responseID)
	msgOpened := false
	var textBuilder strings.Builder
	// oi/ci are assigned lazily when the message actually opens, from the
	// same shared nextIndex counter tool calls use, so a streamed item's
	// output_index always matches its eventual position in the final
	// Output array - including for a tool-only response with no text.
	var oi int
	ci := 0

	type toolState struct {
		id, name string
		args     strings.Builder
		index    int
	}
	tools := map[int32]*toolState{}
	var toolOrder []int32
	nextIndex := 0

	usage := &Usage{}
	stream := out.GetStream()
	defer stream.Close()

	for event := range stream.Events() {
		select {
		case <-sink.Done():
			return &StreamInterruptedError{Reason: "client disconnected"}
		case <-ctx.Done():
			return &StreamInterruptedError{Reason: "request cancelled"}
		default:
		}

		switch v := event.(type) {
		case *brtypes.ConverseStreamOutputMemberContentBlockStart:
			if tu, ok := v.Value.Start.(*brtypes.ContentBlockStartMemberToolUse); ok {
				idx := v.Value.ContentBlockIndex
				st := &toolState{id: aws.ToString(tu.Value.ToolUseId), name: aws.ToString(tu.Value.Name), index: nextIndex}
				nextIndex++
				tools[*idx] = st
				toolOrder = append(toolOrder, *idx)
				outIdx := st.index
				if err := sink.Send(StreamEvent{Type: "response.output_item.added", OutputIndex: &outIdx, Item: &OutputItem{ID: fmt.Sprintf("%s_call_%d", responseID, st.index), Type: "function_call", Status: StatusInProgress, CallID: st.id, Name: st.name}}); err != nil {
					return err
				}
			}
		case *brtypes.ConverseStreamOutputMemberContentBlockDelta:
			idx := v.Value.ContentBlockIndex
			switch d := v.Value.Delta.(type) {
			case *brtypes.ContentBlockDeltaMemberText:
				if !msgOpened {
					oi = nextIndex
					nextIndex++
					if err := sink.Send(StreamEvent{Type: "response.output_item.added", OutputIndex: &oi, Item: &OutputItem{ID: msgItemID, Type: "message", Status: StatusInProgress, Role: "assistant"}}); err != nil {
						return err
					}
					if err := sink.Send(StreamEvent{Type: "response.content_part.added", ItemID: msgItemID, OutputIndex: &oi, ContentIndex: &ci}); err != nil {
						return err
					}
					msgOpened = true
				}
				textBuilder.WriteString(d.Value)
				if err := sink.Send(StreamEvent{Type: "response.output_text.delta", ItemID: msgItemID, OutputIndex: &oi, ContentIndex: &ci, Delta: d.Value}); err != nil {
					return err
				}
			case *brtypes.ContentBlockDeltaMemberToolUse:
				if idx == nil {
					continue
				}
				st, ok := tools[*idx]
				if !ok || d.Value.Input == nil {
					continue
				}
				frag := aws.ToString(d.Value.Input)
				st.args.WriteString(frag)
				itemID := fmt.Sprintf("%s_call_%d", responseID, st.index)
				outIdx := st.index
				if err := sink.Send(StreamEvent{Type: "response.function_call_arguments.delta", ItemID: itemID, OutputIndex: &outIdx, Delta: frag}); err != nil {
					return err
				}
			}
		case *brtypes.ConverseStreamOutputMemberMetadata:
			if v.Value.Usage != nil {
				usage.InputTokens = int(aws.ToInt32(v.Value.Usage.InputTokens))
				usage.OutputTokens = int(aws.ToInt32(v.Value.Usage.OutputTokens))
				usage.TotalTokens = int(aws.ToInt32(v.Value.Usage.TotalTokens))
			}
		}
	}
	if err := stream.Err(); err != nil {
		_ = sink.Send(StreamEvent{Type: "error", Error: &ResponseError{Code: "backend_stream_error", Message: err.Error(), Type: "backend_error"}})
		return &StreamInterruptedError{Reason: err.Error()}
	}

	output := make([]OutputItem, nextIndex)
	if msgOpened {
		finalText := textBuilder.String()
		if err := sink.Send(StreamEvent{Type: "response.output_text.done", ItemID: msgItemID, OutputIndex: &oi, ContentIndex: &ci, Text: finalText}); err != nil {
			return err
		}
		if err := sink.Send(StreamEvent{Type: "response.content_part.done", ItemID: msgItemID, OutputIndex: &oi, ContentIndex: &ci}); err != nil {
			return err
		}
		msgItem := OutputItem{ID: msgItemID, Type: "message", Status: StatusCompleted, Role: "assistant", Content: []ContentPart{{Type: "output_text", Text: finalText}}}
		if err := sink.Send(StreamEvent{Type: "response.output_item.done", OutputIndex: &oi, Item: &msgItem}); err != nil {
			return err
		}
		output[oi] = msgItem
	}
	for _, idx := range toolOrder {
		st := tools[idx]
		itemID := fmt.Sprintf("%s_call_%d", responseID, st.index)
		args := st.args.String()
		outIdx := st.index
		if err := sink.Send(StreamEvent{Type: "response.function_call_arguments.done", ItemID: itemID, OutputIndex: &outIdx, Arguments: args}); err != nil {
			return err
		}
		item := OutputItem{ID: itemID, Type: "function_call", Status: StatusCompleted, CallID: st.id, Name: st.name, Arguments: args}
		if err := sink.Send(StreamEvent{Type: "response.output_item.done", OutputIndex: &outIdx, Item: &item}); err != nil {
			return err
		}
		output[st.index] = item
		output = append(output, item)
	}

	final := &Response{ID: responseID, Object: "response", CreatedAt: now, Status: StatusCompleted, Model: modelID, Output: output, Usage: usage}
	return sink.Send(StreamEvent{Type: "response.completed", Response: final})
}

func translateToBedrock(req Request) ([]brtypes.Message, []brtypes.SystemContentBlock) {
	var messages []brtypes.Message
	var system []brtypes.SystemContentBlock
	if req.Instructions != "" {
		system = append(system, &brtypes.SystemContentBlockMemberText{Value: req.Instructions})
	}
	for _, item := range req.Input {
		switch item.Type {
		case "message":
			text := item.Content.PlainText()
			role := brtypes.ConversationRoleUser
			if item.Role == "assistant" {
				role = brtypes.ConversationRoleAssistant
			}
			messages = append(messages, brtypes.Message{
				Role:    role,
				Content: []brtypes.ContentBlock{&brtypes.ContentBlockMemberText{Value: text}},
			})
		case "function_call":
			// A tool call HarnessMesh's caller (Codex) echoes back from a
			// prior turn. Bedrock's Converse API requires every toolResult
			// to reference a toolUse block in the immediately preceding
			// assistant turn, so this must be translated too, not dropped -
			// otherwise every tool-using multi-turn conversation against
			// this backend fails at the second turn.
			var argsDoc map[string]any
			if item.Arguments != "" {
				_ = json.Unmarshal([]byte(item.Arguments), &argsDoc)
			}
			messages = append(messages, brtypes.Message{
				Role: brtypes.ConversationRoleAssistant,
				Content: []brtypes.ContentBlock{&brtypes.ContentBlockMemberToolUse{Value: brtypes.ToolUseBlock{
					ToolUseId: aws.String(item.CallID),
					Name:      aws.String(item.Name),
					Input:     brdocument.NewLazyDocument(argsDoc),
				}}},
			})
		case "function_call_output":
			messages = append(messages, brtypes.Message{
				Role: brtypes.ConversationRoleUser,
				Content: []brtypes.ContentBlock{&brtypes.ContentBlockMemberToolResult{Value: brtypes.ToolResultBlock{
					ToolUseId: aws.String(item.CallID),
					Content:   []brtypes.ToolResultContentBlock{&brtypes.ToolResultContentBlockMemberText{Value: item.Output}},
				}}},
			})
		}
	}
	return messages, system
}

func translateToolsToBedrock(tools []Tool) *brtypes.ToolConfiguration {
	if len(tools) == 0 {
		return nil
	}
	var specs []brtypes.Tool
	for _, t := range tools {
		if t.Type != "function" {
			continue
		}
		var schema brtypes.ToolInputSchema
		if len(t.Parameters) > 0 {
			var doc map[string]any
			if err := json.Unmarshal(t.Parameters, &doc); err == nil {
				schema = &brtypes.ToolInputSchemaMemberJson{Value: brdocument.NewLazyDocument(doc)}
			}
		}
		specs = append(specs, &brtypes.ToolMemberToolSpec{Value: brtypes.ToolSpecification{
			Name:        aws.String(t.Name),
			Description: aws.String(t.Description),
			InputSchema: schema,
		}})
	}
	if len(specs) == 0 {
		return nil
	}
	return &brtypes.ToolConfiguration{Tools: specs}
}
