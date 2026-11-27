package staticdeploy

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/routes"
)

var hostnameLabel = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// Publish activates a ready version at the site's first public hostname.
func (s *Service) Publish(ctx context.Context, publisher *routes.Publisher, projectID, name string, versionID uuid.UUID, hostname string) (database.StaticSiteVersion, string, error) {
	host, err := publicationHostname(hostname)
	if err != nil {
		return database.StaticSiteVersion{}, "", err
	}
	version, err := s.Version(ctx, projectID, name, versionID)
	if err != nil {
		return database.StaticSiteVersion{}, "", err
	}
	if err := publisher.ActivateStaticSite(ctx, projectID, version, host); err != nil {
		return database.StaticSiteVersion{}, "", err
	}
	return version, host, nil
}

// SwitchVersion activates a ready version on the site's existing public route.
func (s *Service) SwitchVersion(ctx context.Context, publisher *routes.Publisher, projectID, name string, versionID uuid.UUID, expectedRevision time.Time) (database.StaticSitePublication, error) {
	version, err := s.Version(ctx, projectID, name, versionID)
	if err != nil {
		return database.StaticSitePublication{}, err
	}
	return publisher.SwitchStaticSite(ctx, projectID, version, expectedRevision)
}

func publicationHostname(value string) (string, error) {
	if len(value) > 254 {
		return "", fmt.Errorf("static site hostname exceeds its size limit: %w", fs.ErrInvalid)
	}
	host := strings.TrimSuffix(value, ".")
	if len(host) > 253 {
		return "", fmt.Errorf("static site hostname exceeds its size limit: %w", fs.ErrInvalid)
	}
	for label := range strings.SplitSeq(host, ".") {
		if !hostnameLabel.MatchString(label) {
			return "", fmt.Errorf("invalid static site hostname %q: %w", value, fs.ErrInvalid)
		}
	}
	return strings.ToLower(host), nil
}
