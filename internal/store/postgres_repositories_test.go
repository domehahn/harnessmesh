package store

import "testing"

func TestPostgresRepositorySetRequiresDatabase(t *testing.T) {
	if _, err := NewPostgresRepositorySet(nil); err == nil {
		t.Fatal("expected nil database error")
	}
	if got := PostgresSaaSBackendDescriptor(); !got.Distributed || !got.Transactional {
		t.Fatalf("unexpected descriptor: %+v", got)
	}
}
