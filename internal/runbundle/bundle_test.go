package runbundle

import (
	"bytes"
	"testing"
	"time"
)

func TestBuildIsDeterministicAndVerifiable(t *testing.T) {
	files := map[string][]byte{"events.jsonl": []byte(`{"type":"completed"}`), "findings.json": []byte(`[]`)}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	a, _, err := Build("run-1", "tenant-1", "session-1", files, now)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := Build("run-1", "tenant-1", "session-1", files, now)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("bundle output is not deterministic")
	}
	m, err := Verify(a)
	if err != nil || m.RunID != "run-1" || len(m.Files) != 2 {
		t.Fatalf("manifest=%+v err=%v", m, err)
	}
}

func TestRedactRemovesCredentials(t *testing.T) {
	got := string(Redact([]byte(`Authorization: Bearer abc123 token=secret hm_live_abc-123`)))
	for _, secret := range []string{"abc123", "secret", "hm_live_abc-123"} {
		if bytes.Contains([]byte(got), []byte(secret)) {
			t.Fatalf("secret leaked: %q", got)
		}
	}
}
