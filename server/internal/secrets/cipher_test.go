package secrets_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"os"
	"testing"

	"github.com/multica-ai/multica/server/internal/secrets"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, secrets.KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return key
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	c, err := secrets.NewCipher(newKey(t))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}

	plain := []byte("hello secret value with non-ascii: 你好")
	enc, nonce, err := c.Encrypt(plain)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(enc, plain) {
		t.Fatalf("ciphertext contains plaintext")
	}
	if len(nonce) != secrets.NonceSize {
		t.Fatalf("nonce length: got %d, want %d", len(nonce), secrets.NonceSize)
	}

	dec, err := c.Decrypt(enc, nonce)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(dec, plain) {
		t.Fatalf("roundtrip mismatch: got %q want %q", dec, plain)
	}
}

func TestEncrypt_NonceIsRandom(t *testing.T) {
	c, _ := secrets.NewCipher(newKey(t))
	_, n1, _ := c.Encrypt([]byte("x"))
	_, n2, _ := c.Encrypt([]byte("x"))
	if bytes.Equal(n1, n2) {
		t.Fatalf("two Encrypt calls produced the same nonce — RNG is broken or wired wrong")
	}
}

func TestNewCipher_RefusesShortKey(t *testing.T) {
	if _, err := secrets.NewCipher(make([]byte, secrets.KeySize-1)); err == nil {
		t.Fatalf("expected error for short key")
	}
	if _, err := secrets.NewCipher(make([]byte, secrets.KeySize+1)); err == nil {
		t.Fatalf("expected error for long key")
	}
	if _, err := secrets.NewCipher(nil); err == nil {
		t.Fatalf("expected error for nil key")
	}
}

func TestDecrypt_WrongKeyFails(t *testing.T) {
	k1 := newKey(t)
	k2 := newKey(t)
	c1, _ := secrets.NewCipher(k1)
	c2, _ := secrets.NewCipher(k2)
	enc, nonce, _ := c1.Encrypt([]byte("secret"))
	if _, err := c2.Decrypt(enc, nonce); err == nil {
		t.Fatalf("expected decrypt failure with different key")
	}
}

func TestDecrypt_TamperedCiphertextFails(t *testing.T) {
	c, _ := secrets.NewCipher(newKey(t))
	enc, nonce, _ := c.Encrypt([]byte("secret"))
	enc[0] ^= 0xff
	if _, err := c.Decrypt(enc, nonce); err == nil {
		t.Fatalf("expected decrypt failure with tampered ciphertext")
	}
}

func TestDecrypt_WrongNonceLength(t *testing.T) {
	c, _ := secrets.NewCipher(newKey(t))
	enc, _, _ := c.Encrypt([]byte("x"))
	if _, err := c.Decrypt(enc, make([]byte, secrets.NonceSize-1)); err == nil {
		t.Fatalf("expected error for short nonce")
	}
}

func TestNewCipherFromEnv_MissingVarErrors(t *testing.T) {
	t.Setenv("MULTICA_SECRET_MASTER_KEY", "")
	if _, err := secrets.NewCipherFromEnv(); err == nil {
		t.Fatalf("expected error when env var is empty")
	}
}

func TestNewCipherFromEnv_InvalidBase64Errors(t *testing.T) {
	t.Setenv("MULTICA_SECRET_MASTER_KEY", "@@@-not-base64-@@@")
	if _, err := secrets.NewCipherFromEnv(); err == nil {
		t.Fatalf("expected error for invalid base64")
	}
}

func TestNewCipherFromEnv_RoundTrip(t *testing.T) {
	key := make([]byte, secrets.KeySize)
	rand.Read(key)
	encoded := base64.StdEncoding.EncodeToString(key)
	t.Setenv("MULTICA_SECRET_MASTER_KEY", encoded)

	c, err := secrets.NewCipherFromEnv()
	if err != nil {
		t.Fatalf("from env: %v", err)
	}
	enc, nonce, _ := c.Encrypt([]byte("hi"))
	if dec, err := c.Decrypt(enc, nonce); err != nil || string(dec) != "hi" {
		t.Fatalf("roundtrip via env failed: %v %q", err, dec)
	}
}

func TestNewCipherFromEnv_WrongKeyLengthErrors(t *testing.T) {
	t.Setenv("MULTICA_SECRET_MASTER_KEY", base64.StdEncoding.EncodeToString([]byte("only-16-bytes!!!!")))
	if _, err := secrets.NewCipherFromEnv(); err == nil {
		t.Fatalf("expected error for decoded key length != 32")
	}
}

// Sanity: encrypting the empty slice is allowed and roundtrips.
func TestEncryptDecrypt_EmptyPlaintext(t *testing.T) {
	c, _ := secrets.NewCipher(newKey(t))
	enc, nonce, err := c.Encrypt([]byte{})
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	dec, err := c.Decrypt(enc, nonce)
	if err != nil {
		t.Fatalf("decrypt empty: %v", err)
	}
	if len(dec) != 0 {
		t.Fatalf("expected empty plaintext, got %q", dec)
	}
}

// Avoid an unused-import warning if the rest of the tests get rearranged.
var _ = os.Getenv
