package secrets

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
)

func TestVaultRoundTripAndBinding(t *testing.T) {
	ctx := context.Background()
	s, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := make([]byte, 32)
	rand.Read(key)
	v, err := New(s.DB, base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Put(ctx, "acct:a", []byte("hunter2")); err != nil {
		t.Fatal(err)
	}
	var ct []byte
	s.DB.QueryRow(`SELECT ciphertext FROM secrets WHERE ref='acct:a'`).Scan(&ct)
	if bytes.Contains(ct, []byte("hunter2")) {
		t.Fatal("plaintext stored")
	}
	got, err := v.Get(ctx, "acct:a")
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("%q %v", got, err)
	}
	// Ciphertext moved to another ref must not decrypt (ref is authenticated data).
	s.DB.Exec(`INSERT INTO secrets(ref, nonce, ciphertext, key_id, created_at) SELECT 'acct:b', nonce, ciphertext, key_id, created_at FROM secrets WHERE ref='acct:a'`)
	if _, err := v.Get(ctx, "acct:b"); err == nil {
		t.Fatal("swapped ciphertext decrypted")
	}
	// Missing key fails closed.
	empty, _ := New(s.DB, "")
	if _, err := empty.Get(ctx, "acct:a"); err != ErrNoKey {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
	if _, err := New(s.DB, "short"); err == nil {
		t.Fatal("bad key accepted")
	}
}
