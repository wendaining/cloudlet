package database

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const maxSecretValueBytes = 64 << 10

var secretNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

type ProjectSecret struct {
	ID            string
	ProjectID     string
	Name          string
	LatestVersion int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type ProjectSecretVersion struct {
	ID        string
	SecretID  string
	Name      string
	Version   int
	CreatedAt time.Time
}

type ProjectSecretValue struct {
	ProjectSecretVersion
	Value string
}

// CreateProjectSecretVersion encrypts one value and appends it to the named
// project secret. The row lock on project_secrets serializes concurrent
// versions of the same name.
func (s *Store) CreateProjectSecretVersion(ctx context.Context, projectID, name, value string, key []byte) (ProjectSecretVersion, error) {
	if err := validateSecret(projectID, name, value); err != nil {
		return ProjectSecretVersion{}, err
	}
	nonce, ciphertext, err := encryptSecret(key, projectID, name, value)
	if err != nil {
		return ProjectSecretVersion{}, fmt.Errorf("encrypt project secret %q: %w", name, err)
	}
	var result ProjectSecretVersion
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`INSERT INTO project_secrets (id, project_id, name) VALUES (?, ?, ?) ON CONFLICT (project_id, name) DO NOTHING`,
			uuid.NewString(), projectID, name,
		).Error; err != nil {
			return fmt.Errorf("create project secret %q: %w", name, err)
		}
		var secret struct {
			ID            string
			LatestVersion int
		}
		if err := tx.Raw(
			`SELECT id, latest_version FROM project_secrets WHERE project_id = ? AND name = ? FOR UPDATE`,
			projectID, name,
		).Row().Scan(&secret.ID, &secret.LatestVersion); err != nil {
			return fmt.Errorf("lock project secret %q: %w", name, err)
		}
		result = ProjectSecretVersion{
			ID:       uuid.NewString(),
			SecretID: secret.ID,
			Name:     name,
			Version:  secret.LatestVersion + 1,
		}
		var createdAt time.Time
		if err := tx.Raw(
			`INSERT INTO project_secret_versions (id, secret_id, version, nonce, ciphertext) VALUES (?, ?, ?, ?, ?) RETURNING created_at`,
			result.ID, result.SecretID, result.Version, nonce, ciphertext,
		).Row().Scan(&createdAt); err != nil {
			return fmt.Errorf("insert project secret %q version %d: %w", name, result.Version, err)
		}
		result.CreatedAt = createdAt
		if err := tx.Exec(
			`UPDATE project_secrets SET latest_version = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			result.Version, result.SecretID,
		).Error; err != nil {
			return fmt.Errorf("update project secret %q latest version: %w", name, err)
		}
		return nil
	})
	if err != nil {
		return ProjectSecretVersion{}, err
	}
	return result, nil
}

func (s *Store) ProjectSecrets(ctx context.Context, projectID string) ([]ProjectSecret, error) {
	if _, err := uuid.Parse(projectID); err != nil {
		return nil, fmt.Errorf("list project secrets: project ID %q is invalid: %w", projectID, fs.ErrInvalid)
	}
	var secrets []ProjectSecret
	err := s.DB.WithContext(ctx).
		Table("project_secrets").
		Select("id, project_id, name, latest_version, created_at, updated_at").
		Where("project_id = ?", projectID).
		Order("name").
		Scan(&secrets).Error
	return secrets, err
}

func (s *Store) ProjectSecretVersions(ctx context.Context, projectID, name string) ([]ProjectSecretVersion, error) {
	if _, err := uuid.Parse(projectID); err != nil {
		return nil, fmt.Errorf("list project secret versions: project ID %q is invalid: %w", projectID, fs.ErrInvalid)
	}
	if !secretNamePattern.MatchString(name) {
		return nil, fmt.Errorf("list project secret versions: name %q is invalid: %w", name, fs.ErrInvalid)
	}
	var versions []ProjectSecretVersion
	err := s.DB.WithContext(ctx).
		Table("project_secret_versions").
		Select("project_secret_versions.id, project_secret_versions.secret_id, project_secrets.name, project_secret_versions.version, project_secret_versions.created_at").
		Joins("JOIN project_secrets ON project_secrets.id = project_secret_versions.secret_id").
		Where("project_secrets.project_id = ? AND project_secrets.name = ?", projectID, name).
		Order("project_secret_versions.version DESC").
		Scan(&versions).Error
	return versions, err
}

// ProjectSecretValue decrypts one version scoped to the project. A version in
// another project is reported as not found.
func (s *Store) ProjectSecretValue(ctx context.Context, projectID, versionID string, key []byte) (ProjectSecretValue, error) {
	if _, err := uuid.Parse(projectID); err != nil {
		return ProjectSecretValue{}, fmt.Errorf("read project secret version: project ID %q is invalid: %w", projectID, fs.ErrInvalid)
	}
	if _, err := uuid.Parse(versionID); err != nil {
		return ProjectSecretValue{}, fmt.Errorf("read project secret version: version ID %q is invalid: %w", versionID, fs.ErrInvalid)
	}
	var stored struct {
		ProjectSecretVersion
		Nonce      []byte
		Ciphertext []byte
	}
	err := s.DB.WithContext(ctx).
		Table("project_secret_versions").
		Select("project_secret_versions.id, project_secret_versions.secret_id, project_secrets.name, project_secret_versions.version, project_secret_versions.created_at, project_secret_versions.nonce, project_secret_versions.ciphertext").
		Joins("JOIN project_secrets ON project_secrets.id = project_secret_versions.secret_id").
		Where("project_secret_versions.id = ? AND project_secrets.project_id = ?", versionID, projectID).
		Take(&stored).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ProjectSecretValue{}, err
	}
	if err != nil {
		return ProjectSecretValue{}, err
	}
	value, err := decryptSecret(key, projectID, stored.Name, stored.Nonce, stored.Ciphertext)
	if err != nil {
		return ProjectSecretValue{}, fmt.Errorf("decrypt project secret %q version %d: %w", stored.Name, stored.Version, err)
	}
	return ProjectSecretValue{ProjectSecretVersion: stored.ProjectSecretVersion, Value: value}, nil
}

// ProjectSecretValueByName decrypts one version selected by the project, name
// and version number.
func (s *Store) ProjectSecretValueByName(ctx context.Context, projectID, name string, version int, key []byte) (ProjectSecretValue, error) {
	if _, err := uuid.Parse(projectID); err != nil {
		return ProjectSecretValue{}, fmt.Errorf("read project secret version: project ID %q is invalid: %w", projectID, fs.ErrInvalid)
	}
	if !secretNamePattern.MatchString(name) {
		return ProjectSecretValue{}, fmt.Errorf("read project secret version: name %q is invalid: %w", name, fs.ErrInvalid)
	}
	if version <= 0 {
		return ProjectSecretValue{}, fmt.Errorf("read project secret version: version must be positive: %w", fs.ErrInvalid)
	}
	var stored struct {
		ProjectSecretVersion
		Nonce      []byte
		Ciphertext []byte
	}
	err := s.DB.WithContext(ctx).
		Table("project_secret_versions").
		Select("project_secret_versions.id, project_secret_versions.secret_id, project_secrets.name, project_secret_versions.version, project_secret_versions.created_at, project_secret_versions.nonce, project_secret_versions.ciphertext").
		Joins("JOIN project_secrets ON project_secrets.id = project_secret_versions.secret_id").
		Where("project_secrets.project_id = ? AND project_secrets.name = ? AND project_secret_versions.version = ?", projectID, name, version).
		Take(&stored).Error
	if err != nil {
		return ProjectSecretValue{}, err
	}
	value, err := decryptSecret(key, projectID, stored.Name, stored.Nonce, stored.Ciphertext)
	if err != nil {
		return ProjectSecretValue{}, fmt.Errorf("decrypt project secret %q version %d: %w", stored.Name, stored.Version, err)
	}
	return ProjectSecretValue{ProjectSecretVersion: stored.ProjectSecretVersion, Value: value}, nil
}

func validateSecret(projectID, name, value string) error {
	if _, err := uuid.Parse(projectID); err != nil {
		return fmt.Errorf("validate project secret: project ID %q is invalid: %w", projectID, fs.ErrInvalid)
	}
	if !secretNamePattern.MatchString(name) {
		return fmt.Errorf("validate project secret: name %q must start with a letter or underscore and contain at most 128 ASCII letters, digits or underscores: %w", name, fs.ErrInvalid)
	}
	if value == "" || len(value) > maxSecretValueBytes {
		return fmt.Errorf("validate project secret: value must contain 1 to %d bytes: %w", maxSecretValueBytes, fs.ErrInvalid)
	}
	return nil
}

func encryptSecret(key []byte, projectID, name, value string) ([]byte, []byte, error) {
	aead, err := secretAEAD(key)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, []byte(value), secretAssociatedData(projectID, name))
	return nonce, ciphertext, nil
}

func decryptSecret(key []byte, projectID, name string, nonce, ciphertext []byte) (string, error) {
	aead, err := secretAEAD(key)
	if err != nil {
		return "", err
	}
	if len(nonce) != aead.NonceSize() {
		return "", fmt.Errorf("nonce length %d is invalid: %w", len(nonce), fs.ErrInvalid)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, secretAssociatedData(projectID, name))
	if err != nil {
		return "", fmt.Errorf("authenticate ciphertext: %w", err)
	}
	return string(plaintext), nil
}

func secretAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key must contain 32 bytes: %w", fs.ErrInvalid)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func secretAssociatedData(projectID, name string) []byte {
	return []byte(projectID + "\x00" + name)
}
