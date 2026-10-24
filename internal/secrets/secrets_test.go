package secrets

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/domehahn/harnessmesh/internal/store"
)

func TestLocalStoresCiphertextAndListsMetadataOnly(t *testing.T) {
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "secrets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	manager, err := NewLocal(st, "test-master-key")
	if err != nil {
		t.Fatal(err)
	}
	item, err := manager.Put(context.Background(), "tenant-a", "OPENAI_API_KEY", "super-secret", map[string]string{"provider": "openai"})
	if err != nil {
		t.Fatal(err)
	}
	if item.Name != "OPENAI_API_KEY" || item.Backend != "local-encrypted" {
		t.Fatalf("unexpected item: %+v", item)
	}
	items, err := manager.List(context.Background(), "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Name != "OPENAI_API_KEY" {
		t.Fatalf("unexpected list: %+v", items)
	}
	if items[0].Metadata["provider"] != "openai" {
		t.Fatalf("metadata was not retained: %+v", items[0].Metadata)
	}
	secret, err := st.GetSecret(context.Background(), "tenant-a", "OPENAI_API_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if secret == nil || string(secret.Ciphertext) == "super-secret" {
		t.Fatal("plaintext secret was persisted")
	}
}
