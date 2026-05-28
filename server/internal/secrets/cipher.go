// Package secrets provides NaCl secretbox encryption for control-plane
// credentials. The master key is loaded once at server boot from
// MULTICA_SECRET_MASTER_KEY (base64-encoded, exactly 32 bytes after decode).
//
// Losing the master key means every previously-encrypted value is unrecoverable
// and must be re-entered — DRI is expected to keep a backup in a password
// manager. The cipher is process-local; no KMS round-trip and no rotation
// mechanism in this version. See plan 4 (PR D) for the threat model.
package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"golang.org/x/crypto/nacl/secretbox"
)

// Sizes are dictated by NaCl secretbox.
const (
	KeySize   = 32
	NonceSize = 24
)

// EnvVar is the environment variable that holds the base64-encoded master key.
const EnvVar = "MULTICA_SECRET_MASTER_KEY"

// Cipher seals and opens secretbox messages with a fixed 32-byte key.
type Cipher struct {
	key [KeySize]byte
}

// NewCipher copies a 32-byte key into a Cipher. The caller may zero the input
// slice after the call returns.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("master key must be exactly %d bytes (got %d)", KeySize, len(key))
	}
	c := &Cipher{}
	copy(c.key[:], key)
	return c, nil
}

// NewCipherFromEnv reads MULTICA_SECRET_MASTER_KEY, base64-decodes it, and
// builds a Cipher. Any failure returns an error suitable for surfacing at boot.
func NewCipherFromEnv() (*Cipher, error) {
	v := os.Getenv(EnvVar)
	if v == "" {
		return nil, fmt.Errorf("%s not set — control plane requires a master key (generate with: openssl rand -base64 32)", EnvVar)
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid base64: %w", EnvVar, err)
	}
	return NewCipher(raw)
}

// Encrypt seals plain with a fresh random nonce and returns ciphertext + nonce.
// The same plaintext encrypted twice produces different ciphertexts because
// the nonce is sampled from crypto/rand on every call.
func (c *Cipher) Encrypt(plain []byte) (ciphertext, nonce []byte, err error) {
	var n [NonceSize]byte
	if _, err := rand.Read(n[:]); err != nil {
		return nil, nil, fmt.Errorf("nonce rand: %w", err)
	}
	out := secretbox.Seal(nil, plain, &n, &c.key)
	return out, n[:], nil
}

// Decrypt opens ciphertext using the supplied nonce. Returns an error on auth
// failure (wrong key, tampered ciphertext, or wrong nonce).
func (c *Cipher) Decrypt(ciphertext, nonce []byte) ([]byte, error) {
	if len(nonce) != NonceSize {
		return nil, fmt.Errorf("nonce must be %d bytes (got %d)", NonceSize, len(nonce))
	}
	var n [NonceSize]byte
	copy(n[:], nonce)
	out, ok := secretbox.Open(nil, ciphertext, &n, &c.key)
	if !ok {
		return nil, errors.New("decrypt failed — wrong key or corrupted ciphertext")
	}
	return out, nil
}
