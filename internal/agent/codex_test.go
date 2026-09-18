package agent

import "testing"

func TestParseCodexJSONL(t *testing.T) {
	raw := `{"type":"thread.started","thread_id":"0199-test"}
{"type":"item.completed","item":{"id":"x","type":"agent_message","text":"hello"}}
{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`
	thread, usage, text := parseCodexJSONL(raw)
	if thread != "0199-test" {
		t.Fatalf("thread=%q", thread)
	}
	if text != "hello" {
		t.Fatalf("text=%q", text)
	}
	if usage["input_tokens"].(float64) != 10 {
		t.Fatalf("usage=%v", usage)
	}
}
