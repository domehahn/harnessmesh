package artifacts

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestLocalStoreTenantIsolationAndAtomicObject(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.Put(context.Background(), "tenant-a", "evidence/one", strings.NewReader("proof"))
	if err != nil {
		t.Fatal(err)
	}
	if o.Size != 5 || o.SHA256 == "" {
		t.Fatalf("unexpected object metadata: %+v", o)
	}
	r, _, err := s.Get(context.Background(), "tenant-a", "evidence/one")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r)
	r.Close()
	if string(b) != "proof" {
		t.Fatalf("unexpected payload %q", b)
	}
	if _, _, err := s.Get(context.Background(), "tenant-b", "evidence/one"); err == nil {
		t.Fatal("cross-tenant object access unexpectedly succeeded")
	}
	if _, err := s.Put(context.Background(), "tenant-a", "../escape", strings.NewReader("x")); err == nil {
		t.Fatal("path traversal unexpectedly accepted")
	}
}
