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
	version := ProjectSecretVersion{ID: uuid.NewString(), SecretID: uuid.NewString(), Name: "DATABASE_URL", Version: 1}

	nonce, ciphertext, err := encryptSecret(key, projectID, version, value)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(value)) {
		t.Fatal("ciphertext contains the plaintext")
	}
	decrypted, err := decryptSecret(key, projectID, version, nonce, ciphertext)
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
	version := ProjectSecretVersion{ID: uuid.NewString(), SecretID: uuid.NewString(), Name: "TOKEN", Version: 1}
	nonce, ciphertext, err := encryptSecret(key, projectID, version, "secret")
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
			if _, err := decryptSecret(tt.key, tt.pid, ProjectSecretVersion{ID: version.ID, SecretID: version.SecretID, Name: tt.secret, Version: version.Version}, tt.nonce, tt.data); err == nil {
				t.Fatal("invalid ciphertext was accepted")
			}
		})
	}
}

func TestSecretEncryptionUsesUniqueNonces(t *testing.T) {
	key := randomKey(t)
	projectID := uuid.NewString()
	version := ProjectSecretVersion{ID: uuid.NewString(), SecretID: uuid.NewString(), Name: "TOKEN", Version: 1}
	firstNonce, firstCiphertext, err := encryptSecret(key, projectID, version, "same")
	if err != nil {
		t.Fatal(err)
	}
	secondNonce, secondCiphertext, err := encryptSecret(key, projectID, version, "same")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(firstNonce, secondNonce) || bytes.Equal(firstCiphertext, secondCiphertext) {
		t.Fatal("repeated encryption reused nonce or ciphertext")
	}
}

func TestSecretEncryptionAuthenticatesVersionIdentity(t *testing.T) {
	key := randomKey(t)
	projectID := uuid.NewString()
	version := ProjectSecretVersion{ID: uuid.NewString(), SecretID: uuid.NewString(), Name: "TOKEN", Version: 1}
	nonce, ciphertext, err := encryptSecret(key, projectID, version, "value")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"ID", "SecretID", "Version"} {
		t.Run(field, func(t *testing.T) {
			other := version
			switch field {
			case "ID":
				other.ID = uuid.NewString()
			case "SecretID":
				other.SecretID = uuid.NewString()
			case "Version":
				other.Version++
			}
			if _, err := decryptSecret(key, projectID, other, nonce, ciphertext); err == nil {
				t.Fatal("changed version identity authenticated successfully")
			}
		})
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
		{"byte boundary", projectID, "TOKEN", strings.Repeat("a", MaxSecretValueBytes), true},
		{"UTF-8 boundary", projectID, "TOKEN", strings.Repeat("汉", 21845) + "a", true},
		{"UTF-8 over limit", projectID, "TOKEN", strings.Repeat("汉", 21846), false},
		{"value too long", projectID, "TOKEN", strings.Repeat("a", MaxSecretValueBytes+1), false},
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
