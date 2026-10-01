package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/domehahn/harnessmesh/internal/config"
)

// TestE2E_FilesystemWriteEditDelete uses the production provider HTTP,
// SIWC normalization, raw SSE relay, and replay paths against a local fake
// Responses service while performing the actual filesystem lifecycle in a
// disposable git workspace. Ten repetitions make cleanup and ID leakage
// regressions visible under the normal test and race gates.
func TestE2E_FilesystemWriteEditDelete(t *testing.T) {
	for run := 0; run < 10; run++ {
		t.Run(fmt.Sprintf("run_%02d", run+1), func(t *testing.T) {
			workspace := t.TempDir()
			gitRun(t, workspace, "init", "--quiet")
			baseline := gitStatus(t, workspace)

			var mu sync.Mutex
			var captured [][]byte
			turn := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
				mu.Lock()
				captured = append(captured, body)
				turn++
				current := turn
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(filesystemTurnSSE(current)))
			}))
			t.Cleanup(upstream.Close)

			tokenPath := filepath.Join(t.TempDir(), "auth.json")
			if err := saveSIWCTokenSet(tokenPath, &SIWCTokenSet{ClientID: "test", AccessToken: "fake", ExpiresAt: fixedFutureExpiry()}); err != nil {
				t.Fatal(err)
			}
			cfg := config.ProviderGatewayConfig{Enabled: true, Token: "provider-token", DefaultBackend: "chatgpt", Backends: map[string]config.ProviderBackendConfig{"chatgpt": {Type: "chatgpt-subscription"}}}
			reg, err := NewRegistry(cfg, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			sub := reg.backends["chatgpt"].(*SubscriptionBackend)
			sub.tokenPath = tokenPath
			sub.client = &siwcTokenClient{tokenURL: upstream.URL, responsesURL: upstream.URL, httpClient: http.DefaultClient}
			h := NewServer(cfg, reg).Handler()
			file := filepath.Join(workspace, ".harnessmesh-e2e-write-test.txt")

			first := doRawProviderReq(t, h, "provider-token", filesystemRequest("start", ""))
			assertContains(t, first.Body.String(), `"call_id":"create_call"`)
			if err := os.WriteFile(file, []byte("ALPHA\nORIGINAL\n"), 0600); err != nil {
				t.Fatal(err)
			}
			assertFile(t, file, "ALPHA\nORIGINAL\n")

			second := doRawProviderReq(t, h, "provider-token", filesystemRequest("create_call", `{"ok":true}`))
			assertContains(t, second.Body.String(), `"call_id":"patch_call"`)
			data, err := os.ReadFile(file)
			if err != nil || string(data) != "ALPHA\nORIGINAL\n" {
				t.Fatalf("unexpected pre-patch bytes: %q err=%v", data, err)
			}
			if err := os.WriteFile(file, bytes.Replace(data, []byte("ORIGINAL"), []byte("PATCHED"), 1), 0600); err != nil {
				t.Fatal(err)
			}
			assertFile(t, file, "ALPHA\nPATCHED\n")

			third := doRawProviderReq(t, h, "provider-token", filesystemRequest("patch_call", `{"ok":true}`))
			assertContains(t, third.Body.String(), `"call_id":"verify_call"`)
			assertFile(t, file, "ALPHA\nPATCHED\n")
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
			final := doRawProviderReq(t, h, "provider-token", filesystemRequest("verify_call", `{"content":"ALPHA\\nPATCHED\\n","deleted":true}`))
			assertContains(t, final.Body.String(), "HARNESSMESH_WRITE_PATCH_OK")
			if _, err := os.Stat(file); !os.IsNotExist(err) {
				t.Fatalf("temporary file still exists: %v", err)
			}
			if got := gitStatus(t, workspace); got != baseline {
				t.Fatalf("final git status differs from baseline: baseline=%q final=%q", baseline, got)
			}

			mu.Lock()
			bodies := append([][]byte(nil), captured...)
			mu.Unlock()
			if len(bodies) != 4 {
				t.Fatalf("expected four replay requests, got %d", len(bodies))
			}
			assertReplayIDs(t, bodies)
		})
	}
}

func filesystemRequest(callID, output string) string {
	input := `[{"type":"message","role":"user","content":"perform the filesystem task"}]`
	if callID != "start" {
		encoded, _ := json.Marshal(output)
		input = fmt.Sprintf(`[{"type":"function_call","id":"%s_item","call_id":"%s","name":"filesystem_tool","arguments":"{}"},{"type":"function_call_output","call_id":"%s","output":%s}]`, callID, callID, callID, encoded)
	}
	return fmt.Sprintf(`{"model":"gpt-5.6-luna","input":%s,"stream":true,"store":false}`, input)
}

func filesystemTurnSSE(turn int) string {
	if turn == 4 {
		return strings.Join([]string{
			`event: response.created`, `data: {"type":"response.created","response":{"id":"resp_final","status":"in_progress","output":[]}}`, "",
			`event: response.output_text.delta`, `data: {"type":"response.output_text.delta","delta":"HARNESSMESH_WRITE_PATCH_OK"}`, "",
			`event: response.completed`, `data: {"type":"response.completed","response":{"id":"resp_final","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"HARNESSMESH_WRITE_PATCH_OK"}]}]}}`, "", "",
		}, "\n")
	}
	call := []string{"create_call", "patch_call", "verify_call"}[turn-1]
	return strings.Join([]string{
		`event: response.created`, fmt.Sprintf(`data: {"type":"response.created","response":{"id":"resp_%d","status":"in_progress","output":[]}}`, turn), "",
		`event: response.output_item.added`, fmt.Sprintf(`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"%s_item","type":"function_call","status":"in_progress","call_id":"%s","name":"filesystem_tool","arguments":"{}"}}`, call, call), "",
		`event: response.function_call_arguments.done`, fmt.Sprintf(`data: {"type":"response.function_call_arguments.done","output_index":0,"item_id":"%s_item","arguments":"{}"}`, call), "",
		`event: response.output_item.done`, fmt.Sprintf(`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"%s_item","type":"function_call","status":"completed","call_id":"%s","name":"filesystem_tool","arguments":"{}"}}`, call, call), "",
		`event: response.completed`, fmt.Sprintf(`data: {"type":"response.completed","response":{"id":"resp_%d","status":"completed","output":[{"id":"%s_item","type":"function_call","call_id":"%s","name":"filesystem_tool","arguments":"{}"}]}}`, turn, call, call), "", "",
	}, "\n")
}

func assertReplayIDs(t *testing.T, bodies [][]byte) {
	t.Helper()
	want := []string{"create_call", "patch_call", "verify_call"}
	for i := 1; i < len(bodies); i++ {
		var body struct {
			Input []struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
			} `json:"input"`
		}
		if err := json.Unmarshal(bodies[i], &body); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, item := range body.Input {
			if item.Type == "function_call_output" && item.CallID == want[i-1] {
				found = true
			}
		}
		if !found {
			t.Fatalf("replay %d did not preserve call/output id %q: %s", i, want[i-1], bodies[i])
		}
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("file %s: got %q err=%v want %q", path, data, err, want)
	}
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("expected %q in response", want)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}

func gitStatus(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain=v1")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
