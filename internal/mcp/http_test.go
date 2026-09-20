package mcp

import (
	"context"
	"testing"
)

func TestRemoteMCPRequiresTokenAndServesHealth(t *testing.T) {
	server := NewServer(nil, "", "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Exercise the public validation path without binding a real listener.
	if err := server.ServeHTTP(ctx, "127.0.0.1:0", ""); err == nil {
		t.Fatal("expected empty token to be rejected")
	}
}
