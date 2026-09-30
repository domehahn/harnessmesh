package provider

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/domehahn/harnessmesh/internal/config"
)

// FuzzResponsesRequestParsing proves POST /v1/responses never panics on
// arbitrary/malformed request bodies - it must only ever produce an
// ordinary HTTP error response.
func FuzzResponsesRequestParsing(f *testing.F) {
	f.Add(`{"model":"x","input":"hi"}`)
	f.Add(`{"model":"x","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	f.Add(`not json`)
	f.Add(`{`)
	f.Add(`null`)
	f.Add(`{"model":123}`)
	f.Add(`{"model":"x","input":{"nested":"object instead of string or array"}}`)
	f.Add(`{"model":"x","input":[{"type":"function_call_output","call_id":"c1","output":"result"}]}`)
	f.Add(`{"model":"x","tools":[{"type":"function","name":"f","parameters":{"not":"a schema necessarily"}}]}`)
	f.Add(`{"model":"x","input":"hi","tool_choice":{"type":"function","name":"f"}}`)

	backend := &fakeInferenceBackend{name: "local", typ: "openai-compatible", events: simpleTextEvents("resp_1", "ok")}
	reg := newRegistryForTest(map[string]InferenceBackend{"local": backend}, Policy{ZeroCreditMode: true, DefaultBackend: "local"})
	s := NewServer(testProviderConfig("secret"), reg)
	h := s.Handler()

	f.Fuzz(func(t *testing.T, body string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("handler panicked on body %q: %v", body, r)
			}
		}()
		r := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader([]byte(body)))
		r.Header.Set("Authorization", "Bearer secret")
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code >= 500 {
			t.Fatalf("body %q produced a 5xx (%d): %s", body, rec.Code, rec.Body.String())
		}
	})
}

// FuzzStreamEventJSON proves StreamEvent survives arbitrary JSON round
// trips (as the malformed-event handling path in
// backend_openai_compatible.go must) without panicking.
func FuzzStreamEventJSON(f *testing.F) {
	f.Add(`{"id":"x","choices":[{"delta":{"content":"hi"}}]}`)
	f.Add(`{"id":"x","choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{}"}}]}}]}`)
	f.Add(`not json`)
	f.Add(`{}`)
	f.Add(`{"choices":null}`)
	f.Add(`{"choices":[{}]}`)
	f.Add(`{"usage":{"prompt_tokens":"not a number"}}`)

	f.Fuzz(func(t *testing.T, data string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("chatCompletionsChunk unmarshal panicked on %q: %v", data, r)
			}
		}()
		var chunk chatCompletionsChunk
		_ = json.Unmarshal([]byte(data), &chunk)
	})
}

// FuzzInputItemsUnmarshal proves the flexible "input" field (string or
// array) never panics on arbitrary input.
func FuzzInputItemsUnmarshal(f *testing.F) {
	f.Add(`"plain string"`)
	f.Add(`[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]`)
	f.Add(`123`)
	f.Add(`null`)
	f.Add(`[1,2,3]`)
	f.Add(`[{"type":123}]`)
	f.Add(``)
	f.Add(`[{"role":"user","content":"hi"}]`)
	f.Add(`[{"role":"user","content":123}]`)
	f.Add(`[{"role":"user","content":null}]`)
	f.Add(`[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"x"}]}]`)
	f.Add(`[{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"}]`)
	f.Add(`[{"type":"unknown_future_type","foo":"bar"}]`)
	f.Add(`[{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"f","parameters":{"type":"object","properties":{}}}]}]`)
	f.Add(`[{"type":"additional_tools","role":"developer","tools":[]}]`)
	f.Add(`[{"type":"additional_tools","role":"developer"}]`)
	f.Add(`[{"type":"additional_tools","tools":[{"type":"function","name":"f"}]}]`)
	f.Add(`[{"type":"additional_tools","role":"user","tools":[{"type":"function","name":"f"}]}]`)
	f.Add(`[{"type":"additional_tools","role":"developer","tools":"not-an-array"}]`)
	f.Add(`[{"type":"additional_tools","role":"developer","tools":[123]}]`)
	f.Add(`[{"type":"additional_tools","role":"developer","tools":[{"type":"web_search"}]}]`)
	f.Add(`[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"f"}]}]}]`)
	f.Add(`[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"ns","tools":[{"type":"namespace","name":"inner","tools":[{"type":"function","name":"f"}]}]}]}]`)
	f.Add(`[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","tools":[{"type":"function","name":"f"}]}]}]`)
	f.Add(`[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"ns","tools":[]}]}]`)
	f.Add(`[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"ns","tools":[{"type":"file_search"}]}]}]`)
	f.Add(`[{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"ns","tools":[{"type":"unknown_tool_xyz"}]}]}]`)
	f.Add(`[{"type":"additional_tools","role":"developer","tools":[{"type":"unknown_tool_xyz"}]}]`)
	f.Add(`[{"type":"computer_call","call_id":"c1"}]`)
	f.Add(`[{"type":"apply_patch_call","call_id":"c1","operation":{"type":"update_file","diff":"x","path":"a"}}]`)
	f.Add(`[{"type":"apply_patch_call_output","call_id":"c1","output":"ok"}]`)
	f.Add(`[{"type":"local_shell_call","call_id":"c1","action":{"command":["ls"]}}]`)
	f.Add(`[{"type":"local_shell_call_output","id":"c1","output":"ok"}]`)
	f.Add(`[{"type":"custom_tool_call","call_id":"c1","name":"code_exec","input":"print(1)"}]`)
	f.Add(`[{"type":"custom_tool_call_output","call_id":"c1","output":"1\n"}]`)
	f.Add(`[{"type":"web_search_call","id":"w1","status":"completed"}]`)
	f.Add(`[{"type":"program"}]`)
	f.Add(`[{"type":"program_output"}]`)
	f.Add(`[{"type":"shell_call","call_id":"c1","action":{"commands":["ls"]}}]`)
	f.Add(`[{"type":"shell_call_output","call_id":"c1","output":[]}]`)
	f.Add(`[{"type":"create_subagent_call"}]`)
	f.Add(`[{"type":"send_subagent_input_call"}]`)
	f.Add(`[{"type":"wait_for_subagents_call"}]`)
	f.Add(`[{"type":"interrupt_subagent_call"}]`)
	f.Add(`[{"type":"agent_message"}]`)
	f.Add(`[{"type":"configuration_update","reasoning":{"effort":"high"}}]`)
	f.Add(`[{"type":"compaction_trigger"}]`)

	f.Fuzz(func(t *testing.T, data string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("InputItems unmarshal panicked on %q: %v", data, r)
			}
		}()
		var it InputItems
		_ = json.Unmarshal([]byte(data), &it)
	})
}

// FuzzSIWCNormalize proves normalizeForSIWC never panics on arbitrary
// request bodies, including every documented forbidden-field shape.
func FuzzSIWCNormalize(f *testing.F) {
	f.Add(`{"model":"x","input":"hi"}`)
	f.Add(`{"model":"x","input":"hi","store":true}`)
	f.Add(`{"model":"x","input":"hi","stream":false}`)
	f.Add(`{"model":"x","input":"hi","store":"not-a-bool"}`)
	f.Add(`{"model":"x","input":"hi","stream":123}`)
	f.Add(`{"model":"x","input":"hi","temperature":0.7}`)
	f.Add(`{"model":"x","input":"hi","previous_response_id":"resp_1"}`)
	f.Add(`{"model":"x","input":"hi","metadata":null}`)
	f.Add(`{"model":"x","input":"hi","reasoning":{"effort":"high"}}`)
	f.Add(`not json`)
	f.Add(`{}`)
	f.Add(`null`)

	f.Fuzz(func(t *testing.T, body string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("normalizeForSIWC panicked on body %q: %v", body, r)
			}
		}()
		var req Request
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			return
		}
		_, _ = normalizeForSIWC(req)
	})
}

// FuzzProviderConfigParsing proves the provider-gateway section of config
// parsing never panics on arbitrary structurally-valid-JSON-but-adversarial
// input.
func FuzzProviderConfigParsing(f *testing.F) {
	f.Add(`{"version":2,"agents":{"a":{"adapter":"claude-code","writable":true}},"provider":{"enabled":true,"default_backend":"x","backends":{"x":{"type":"openai-compatible","base_url":"http://a"}}}}`)
	f.Add(`{"version":2,"agents":{"a":{"adapter":"claude-code","writable":true}},"provider":{"enabled":true,"default_backend":"","backends":{}}}`)
	f.Add(`{"version":2,"agents":{"a":{"adapter":"claude-code","writable":true}},"provider":{"enabled":true,"zero_credit_mode":null}}`)
	f.Add(`{"provider":{}}`)
	f.Add(`{}`)

	f.Fuzz(func(t *testing.T, body string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("config parse panicked on %q: %v", body, r)
			}
		}()
		_, _ = config.Parse([]byte(body))
	})
}
