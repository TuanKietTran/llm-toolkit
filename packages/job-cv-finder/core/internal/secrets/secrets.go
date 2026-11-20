// Package secrets stores platform credentials encrypted at rest (AES-256-GCM) and hands
// out opaque references. Plaintext leaves this package only to an executor making the call
// it is scoped to; it is never logged, returned by the API, put in a task payload, or shown
// to a model.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Vault encrypts secrets with a key supplied by the environment (or a secret manager that
// injects it); the key itself is never stored in the database.
type Vault struct {
	db    *sql.DB
	aead  cipher.AEAD
	keyID string
}

// ErrNoKey means JCF_SECRET_KEY was not configured; secret operations are refused.
var ErrNoKey = errors.New("secret key not configured (set JCF_SECRET_KEY to 32 random bytes, base64)")

// New builds a vault from a base64-encoded 32-byte key. An empty key yields a vault that
// refuses every operation, so a misconfigured deployment fails closed.
func New(db *sql.DB, keyB64 string) (*Vault, error) {
	v := &Vault{db: db}
	if keyB64 == "" {
		return v, nil
	}
	key, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("JCF_SECRET_KEY must be base64 of exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	v.aead, err = cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(key)
	v.keyID = hex.EncodeToString(sum[:4])
	return v, nil
}

// Configured reports whether a key is present.
func (v *Vault) Configured() bool { return v.aead != nil }

// Put encrypts and stores plaintext under ref (the ref is bound as additional data, so a
// ciphertext copied to another ref will not decrypt).
func (v *Vault) Put(ctx context.Context, ref string, plaintext []byte) error {
	if v.aead == nil {
		return ErrNoKey
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ct := v.aead.Seal(nil, nonce, plaintext, []byte(ref))
	_, err := v.db.ExecContext(ctx, `INSERT INTO secrets(ref, nonce, ciphertext, key_id, created_at) VALUES(?,?,?,?,?)
		ON CONFLICT(ref) DO UPDATE SET nonce=excluded.nonce, ciphertext=excluded.ciphertext, key_id=excluded.key_id, created_at=excluded.created_at`,
		ref, nonce, ct, v.keyID, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// Get decrypts the secret under ref.
func (v *Vault) Get(ctx context.Context, ref string) ([]byte, error) {
	if v.aead == nil {
		return nil, ErrNoKey
	}
	var nonce, ct []byte
	var kid string
	err := v.db.QueryRowContext(ctx, `SELECT nonce, ciphertext, key_id FROM secrets WHERE ref=?`, ref).Scan(&nonce, &ct, &kid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("secret %s not found", ref)
	}
	if err != nil {
		return nil, err
	}
	if kid != v.keyID {
		return nil, fmt.Errorf("secret %s was encrypted with key %s, current key is %s", ref, kid, v.keyID)
	}
	pt, err := v.aead.Open(nil, nonce, ct, []byte(ref))
	if err != nil {
		return nil, fmt.Errorf("secret %s failed authentication", ref)
	}
	return pt, nil
}
