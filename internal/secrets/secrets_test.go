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

func TestVaultFromEnv_TLSAndProductionValidation(t *testing.T) {
	t.Run("missing env", func(t *testing.T) {
		t.Setenv("VAULT_ADDR", "")
		t.Setenv("VAULT_TOKEN", "")
		if _, err := NewVaultFromEnv(); err == nil {
			t.Fatal("expected error when VAULT_ADDR/VAULT_TOKEN are missing")
		}
	})

	t.Run("production rejects non-https remote vault", func(t *testing.T) {
		t.Setenv("VAULT_ADDR", "http://vault.company.internal:8200")
		t.Setenv("VAULT_TOKEN", "s.test-token")
		t.Setenv("HARNESSMESH_ENV", "production")
		if _, err := NewVaultFromEnv(); err == nil {
			t.Fatal("expected production to reject unencrypted http:// vault address")
		}
	})

	t.Run("production permits local loopback http", func(t *testing.T) {
		t.Setenv("VAULT_ADDR", "http://127.0.0.1:8200")
		t.Setenv("VAULT_TOKEN", "s.test-token")
		t.Setenv("HARNESSMESH_ENV", "production")
		v, err := NewVaultFromEnv()
		if err != nil {
			t.Fatalf("unexpected error for local loopback: %v", err)
		}
		if v.Address != "http://127.0.0.1:8200" {
			t.Fatalf("unexpected address: %s", v.Address)
		}
	})

	t.Run("invalid ca cert path", func(t *testing.T) {
		t.Setenv("VAULT_ADDR", "https://vault.company.internal:8200")
		t.Setenv("VAULT_TOKEN", "s.test-token")
		t.Setenv("VAULT_CACERT", "/nonexistent/vault-ca.pem")
		if _, err := NewVaultFromEnv(); err == nil {
			t.Fatal("expected error for nonexistent CA cert file")
		}
	})
}
