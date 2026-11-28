package containerpublish

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/postgresbinding"
	"gorm.io/gorm"
)

const postgresURLPath = "cloudlet-postgres-url"
const postgresURLFileEnv = "CLOUDLET_POSTGRES_URL_FILE"

var ErrPostgresBindingNotFound = errors.New("项目 PostgreSQL 绑定不存在")
var ErrPostgresBindingUnavailable = errors.New("项目 PostgreSQL 绑定凭据不可用于容器")
var ErrBindingSecretKeyUnavailable = errors.New("容器的项目凭据存储未配置")

type runtimeBinding struct {
	name            *string
	secretVersionID *uuid.UUID
	fileEnv         string
	files           []containers.File
}

func (binding runtimeBinding) environment(env []string) []string {
	if binding.name == nil {
		return env
	}
	return append(slices.Clone(env), binding.fileEnv+"=/"+binding.files[0].Path)
}

// resolvePostgresBinding resolves only credentials owned by this project. A
// restart supplies the original execution's immutable Secret reference.
func (s *Service) resolvePostgresBinding(ctx context.Context, projectID uuid.UUID, name *string, pinnedVersion *uuid.UUID) (runtimeBinding, error) {
	if name == nil {
		return runtimeBinding{}, nil
	}
	metadata, err := s.store.PostgresBinding(ctx, projectID.String(), *name)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return runtimeBinding{}, fmt.Errorf("%w: %q", ErrPostgresBindingNotFound, *name)
	}
	if err != nil {
		return runtimeBinding{}, err
	}
	if len(s.secretKey) == 0 {
		return runtimeBinding{}, ErrBindingSecretKeyUnavailable
	}
	ip, _ := netip.ParseAddr(metadata.Host)
	if ip.Unmap().IsLoopback() || strings.EqualFold(strings.TrimSuffix(metadata.Host, "."), "localhost") {
		return runtimeBinding{}, fmt.Errorf("容器无法使用回环 PostgreSQL 端点: %w", fs.ErrInvalid)
	}
	versionID := metadata.SecretVersionID
	if pinnedVersion != nil {
		versionID = *pinnedVersion
	}
	value, err := s.store.ProjectSecretValue(ctx, projectID.String(), versionID.String(), s.secretKey)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return runtimeBinding{}, fmt.Errorf("%w: %q", ErrPostgresBindingUnavailable, *name)
	}
	if err != nil {
		return runtimeBinding{}, err
	}
	connection, err := postgresbinding.ConnectionURL(metadata.Host, metadata.Port, metadata.Database, value.Value)
	if err != nil {
		// Credential parsing errors can originate from input controlled by the
		// project. Keep credential details out of operation records and logs.
		return runtimeBinding{}, fmt.Errorf("%w: %q", ErrPostgresBindingUnavailable, *name)
	}
	connection.RawQuery = "sslmode=verify-full&sslrootcert=system"
	return runtimeBinding{
		name:            name,
		secretVersionID: &versionID, fileEnv: postgresURLFileEnv,
		files: []containers.File{{Path: postgresURLPath, Data: []byte(connection.String())}},
	}, nil
}
