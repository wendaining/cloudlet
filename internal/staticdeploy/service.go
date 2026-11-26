// Package staticdeploy prepares immutable static site versions for activation.
package staticdeploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/artifacts"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/siteartifact"
	"github.com/wendaining/cloudlet/internal/sitehttp"
)

const MaxUploadBytes = 64 << 20

var (
	siteName           = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	versionDirectory   = regexp.MustCompile(`^\.site-[0-9]+$`)
	ErrInvalidSiteName = errors.New("invalid static site name")
)

type Service struct {
	store    *database.Store
	archives *artifacts.Store
	versions string
}

func New(store *database.Store, directory string) (*Service, error) {
	if store == nil || !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("configure static site storage: database and absolute directory required: %w", fs.ErrInvalid)
	}
	archives, versions := filepath.Join(directory, "archives"), filepath.Join(directory, "versions")
	for _, path := range []string{archives, versions} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return nil, fmt.Errorf("create static site storage directory %q: %w", path, err)
		}
		probe, err := os.CreateTemp(path, ".write-check-")
		if err != nil {
			return nil, fmt.Errorf("check writable static site storage directory %q: %w", path, err)
		}
		if err := errors.Join(probe.Close(), os.Remove(probe.Name())); err != nil {
			return nil, fmt.Errorf("clean static site storage check in %q: %w", path, err)
		}
	}
	// Persist the directories before allowing PostgreSQL to reference their contents.
	for _, path := range []string{directory, filepath.Dir(directory)} {
		handle, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open static site storage directory %q: %w", path, err)
		}
		if err := errors.Join(handle.Sync(), handle.Close()); err != nil {
			return nil, fmt.Errorf("persist static site storage directory %q: %w", path, err)
		}
	}
	artifactStore, err := artifacts.New(archives, MaxUploadBytes)
	if err != nil {
		return nil, err
	}
	return &Service{store: store, archives: artifactStore, versions: versions}, nil
}

func (s *Service) Close() error { return s.archives.Close() }

func validName(name string) bool { return len(name) <= 63 && siteName.MatchString(name) }

func (s *Service) Upload(ctx context.Context, projectID, name string, input io.Reader) (database.StaticSiteVersion, error) {
	if !validName(name) {
		return database.StaticSiteVersion{}, ErrInvalidSiteName
	}
	if err := ctx.Err(); err != nil {
		return database.StaticSiteVersion{}, err
	}
	digest, err := s.archives.Put(input)
	if err != nil {
		return database.StaticSiteVersion{}, fmt.Errorf("store ZIP for site %q: %w", name, err)
	}
	if err := ctx.Err(); err != nil {
		return database.StaticSiteVersion{}, err
	}
	directory, err := siteartifact.Extract(s.archives, digest, s.versions, siteartifact.Limits{MaxEntries: 10_000, MaxBytes: 256 << 20})
	if err != nil {
		return database.StaticSiteVersion{}, err
	}
	version, uncertain, err := s.store.CreateStaticSiteVersion(ctx, projectID, name, digest, filepath.Base(directory))
	if err != nil && !uncertain {
		if removeErr := os.RemoveAll(directory); removeErr != nil {
			err = errors.Join(err, fmt.Errorf("remove unregistered version directory %q: %w", directory, removeErr))
		}
	}
	return version, err
}

func (s *Service) Version(ctx context.Context, projectID, name string, id uuid.UUID) (database.StaticSiteVersion, error) {
	if !validName(name) {
		return database.StaticSiteVersion{}, ErrInvalidSiteName
	}
	return s.store.StaticSiteVersion(ctx, projectID, name, id)
}

// OpenArchive opens the original ZIP for a version scoped to this project and site.
func (s *Service) OpenArchive(ctx context.Context, projectID, name string, id uuid.UUID) (*os.File, string, error) {
	version, err := s.Version(ctx, projectID, name, id)
	if err != nil {
		return nil, "", err
	}
	file, err := s.archives.Open(version.ArtifactDigest)
	if err != nil {
		return nil, "", fmt.Errorf("open original ZIP for site version %s: %w", id, err)
	}
	return file, version.ArtifactDigest, nil
}

// Handler prepares the file handler for a version resolved by the caller.
// Selecting an active version and publishing its route are separate operations.
func (s *Service) Handler(version database.StaticSiteVersion) (http.Handler, error) {
	if !versionDirectory.MatchString(version.Directory) {
		return nil, fmt.Errorf("invalid file directory for static version %s: %w", version.ID, fs.ErrInvalid)
	}
	directory := os.DirFS(filepath.Join(s.versions, version.Directory))
	info, err := fs.Stat(directory, "index.html")
	if err != nil {
		return nil, fmt.Errorf("open files for static version %s: %w", version.ID, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("invalid index.html for static version %s: %w", version.ID, fs.ErrInvalid)
	}
	return sitehttp.New(directory)
}
