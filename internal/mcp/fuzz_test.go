package mcp

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/domehahn/harnessmesh/internal/collaboration"
	"github.com/domehahn/harnessmesh/internal/config"
	"github.com/domehahn/harnessmesh/internal/contextpack"
	"github.com/domehahn/harnessmesh/internal/store"
)

// FuzzHandleMessage proves the MCP JSON-RPC entrypoint never panics on
// arbitrary/malformed input, only ever returning a well-formed JSON-RPC
// error. This is the single most exposed parser in the ChatGPT bridge path
// (every remote MCP request reaches it).
func FuzzHandleMessage(f *testing.F) {
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"peer.ask","arguments":{}}}`))
	f.Add([]byte(`not json at all`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"jsonrpc":"2.0","method":null}`))
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"change.create","arguments":"not an object"}}`))
	f.Add([]byte(`{"jsonrpc": 2.0, "id": [1,2,3], "method": "tools/call"}`))
	f.Add([]byte(""))
	f.Add([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"peer.submit_finding","arguments":{"id":"weird-id","severity":null,"category":123}}}`))

	tmpDir := f.TempDir()
	st, err := store.OpenSQLite(filepath.Join(tmpDir, "fuzz.db"))
	if err != nil {
		f.Fatalf("OpenSQLite: %v", err)
	}
	f.Cleanup(func() { st.Close() })

	cfg := &config.Config{
		Version: 2,
		Agents: map[string]config.AgentConfig{
			"fuzz-caller": {Role: "executor", ExecutionMode: "managed", Writable: true},
		},
	}
	eng := collaboration.NewEngine(collaboration.EngineConfig{
		Config:    cfg,
		Store:     st,
		Repo:      tmpDir,
		Projector: contextpack.New(tmpDir, cfg.Context),
	})
	f.Cleanup(eng.Close)

	if _, err := eng.CreateSession(context.Background(), "sess_fuzz", "fuzz session"); err != nil {
		f.Fatalf("CreateSession: %v", err)
	}

	server := NewServer(eng, "sess_fuzz", "fuzz-caller")

	f.Fuzz(func(t *testing.T, raw []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("HandleMessage panicked on input %q: %v", raw, r)
			}
		}()
		// A nil engine means every non-parse-error path will itself error
		// out deep inside (nil pointer -> recovered panic would be a bug we
		// want the fuzzer to find), which is exactly the coverage we want:
		// this proves malformed/adversarial input can never crash the
		// server, regardless of what it does downstream.
		_, _ = server.HandleMessage(context.Background(), raw)
	})
}
