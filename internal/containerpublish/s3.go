package containerpublish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/s3binding"
	"gorm.io/gorm"
)

const s3ConfigPath = "cloudlet-s3-config.json"
const s3ConfigFileEnv = "CLOUDLET_S3_CONFIG_FILE"

var ErrS3BindingNotFound = errors.New("项目 S3 绑定不存在")
var ErrS3BindingUnavailable = errors.New("项目 S3 绑定凭据不可用于容器")

type s3RuntimeConfig struct {
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
	Bucket   string `json:"bucket"`
	s3binding.Credentials
}

func (s *Service) resolveS3Binding(ctx context.Context, projectID uuid.UUID, name *string, pinnedVersion *uuid.UUID) (runtimeBinding, error) {
	if name == nil {
		return runtimeBinding{}, nil
	}
	metadata, err := s.store.S3Binding(ctx, projectID.String(), *name)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return runtimeBinding{}, fmt.Errorf("%w: %q", ErrS3BindingNotFound, *name)
	}
	if err != nil {
		return runtimeBinding{}, err
	}
	if len(s.secretKey) == 0 {
		return runtimeBinding{}, ErrBindingSecretKeyUnavailable
	}
	if err := validateContainerS3Endpoint(metadata.Endpoint); err != nil {
		return runtimeBinding{}, err
	}
	versionID := metadata.SecretVersionID
	if pinnedVersion != nil {
		versionID = *pinnedVersion
	}
	value, err := s.store.ProjectSecretValue(ctx, projectID.String(), versionID.String(), s.secretKey)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return runtimeBinding{}, fmt.Errorf("%w: %q", ErrS3BindingUnavailable, *name)
	}
	if err != nil {
		return runtimeBinding{}, err
	}
	credentials, err := s3binding.DecodeCredentials(value.Value)
	if err != nil {
		return runtimeBinding{}, fmt.Errorf("%w: %q", ErrS3BindingUnavailable, *name)
	}
	contents, err := json.Marshal(s3RuntimeConfig{
		Endpoint: metadata.Endpoint, Region: metadata.Region, Bucket: metadata.Bucket, Credentials: credentials,
	})
	if err != nil {
		return runtimeBinding{}, fmt.Errorf("encode container S3 configuration: %w", err)
	}
	return runtimeBinding{
		name: name, secretVersionID: &versionID, fileEnv: s3ConfigFileEnv,
		files: []containers.File{{Path: s3ConfigPath, Data: contents}},
	}, nil
}

func validateContainerS3Endpoint(value string) error {
	endpoint, err := url.Parse(value)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" {
		return fmt.Errorf("容器 S3 端点必须使用 HTTPS: %w", fs.ErrInvalid)
	}
	ip, _ := netip.ParseAddr(endpoint.Hostname())
	if ip.Unmap().IsLoopback() || strings.EqualFold(strings.TrimSuffix(endpoint.Hostname(), "."), "localhost") {
		return fmt.Errorf("容器无法使用回环 S3 端点: %w", fs.ErrInvalid)
	}
	return nil
}
