package database

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSecretEncryptionRoundTrip(t *testing.T) {
	key := randomKey(t)
	projectID := uuid.NewString()
	value := "database-password-版本一"

	nonce, ciphertext, err := encryptSecret(key, projectID, "DATABASE_URL", value)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(value)) {
		t.Fatal("ciphertext contains the plaintext")
	}
	decrypted, err := decryptSecret(key, projectID, "DATABASE_URL", nonce, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	if decrypted != value {
		t.Fatalf("decrypted value = %q, want %q", decrypted, value)
	}
}

func TestSecretEncryptionRejectsWrongKeyOrContext(t *testing.T) {
	key := randomKey(t)
	projectID := uuid.NewString()
	nonce, ciphertext, err := encryptSecret(key, projectID, "TOKEN", "secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		key    []byte
		pid    string
		secret string
		nonce  []byte
		data   []byte
	}{
		{"wrong key", randomKey(t), projectID, "TOKEN", nonce, ciphertext},
		{"wrong project", key, uuid.NewString(), "TOKEN", nonce, ciphertext},
		{"wrong name", key, projectID, "OTHER", nonce, ciphertext},
		{"tampered", key, projectID, "TOKEN", nonce, append(bytes.Clone(ciphertext), 0)},
		{"short nonce", key, projectID, "TOKEN", nonce[:4], ciphertext},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decryptSecret(tt.key, tt.pid, tt.secret, tt.nonce, tt.data); err == nil {
				t.Fatal("invalid ciphertext was accepted")
			}
		})
	}
}

func TestSecretEncryptionUsesUniqueNonces(t *testing.T) {
	key := randomKey(t)
	projectID := uuid.NewString()
	firstNonce, firstCiphertext, err := encryptSecret(key, projectID, "TOKEN", "same")
	if err != nil {
		t.Fatal(err)
	}
	secondNonce, secondCiphertext, err := encryptSecret(key, projectID, "TOKEN", "same")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(firstNonce, secondNonce) || bytes.Equal(firstCiphertext, secondCiphertext) {
		t.Fatal("repeated encryption reused nonce or ciphertext")
	}
}

func TestSecretValidation(t *testing.T) {
	projectID := uuid.NewString()
	for _, tt := range []struct {
		name    string
		project string
		secret  string
		value   string
		valid   bool
	}{
		{"valid", projectID, "DATABASE_URL", "value", true},
		{"underscore", projectID, "_TOKEN", "value", true},
		{"invalid project", "not-a-uuid", "TOKEN", "value", false},
		{"empty name", projectID, "", "value", false},
		{"digit first", projectID, "1TOKEN", "value", false},
		{"dash", projectID, "DATABASE-URL", "value", false},
		{"too long name", projectID, strings.Repeat("A", 129), "value", false},
		{"empty value", projectID, "TOKEN", "", false},
		{"value too long", projectID, "TOKEN", strings.Repeat("a", maxSecretValueBytes+1), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSecret(tt.project, tt.secret, tt.value)
			if (err == nil) != tt.valid {
				t.Fatalf("valid = %v, validation returned %v", tt.valid, err)
			}
			if err != nil && !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("validation error = %v, want fs.ErrInvalid", err)
			}
		})
	}
}

func TestSecretAEADRejectsInvalidKey(t *testing.T) {
	if _, err := secretAEAD([]byte("short")); err == nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("secretAEAD error = %v", err)
	}
}

func randomKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}
