package browser

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
	digest "github.com/opencontainers/go-digest"
	"github.com/wendaining/cloudlet/internal/artifacts"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/executions"
)

const (
	runtimePort            = 8080
	defaultSessionLifetime = 5 * time.Minute
	cleanupTimeout         = 30 * time.Second
)

var ErrSessionUnavailable = errors.New("浏览器会话未就绪或已结束")

type Service struct {
	screenshots *artifacts.Store
	downloads   *artifacts.Store
	store       *database.Store
	capacity    *capacity.Store
	executions  *executions.Service
	adapter     *containers.Adapter
	http        *http.Client
	hostID      string
	image       string
	profile     string
	key         []byte
	starting    sync.Map // session ID to a channel closed when startup and any rollback finish
}

func NewService(ctx context.Context, store *database.Store, docker *client.Client, hostID, imageID, profilePath, screenshotDirectory string, key []byte) (*Service, error) {
	if store == nil || docker == nil || len(key) != 32 {
		return nil, fmt.Errorf("Browser Run requires PostgreSQL, Docker and a 32-byte platform key: %w", fs.ErrInvalid)
	}
	if !strings.HasPrefix(docker.DaemonHost(), "unix://") {
		return nil, fmt.Errorf("Browser Run requires a local Unix Docker socket: %w", fs.ErrInvalid)
	}
	identity, err := digest.Parse(imageID)
	if err != nil || identity.Algorithm() != digest.SHA256 {
		return nil, fmt.Errorf("Browser Run requires an immutable local SHA-256 image ID: %w", fs.ErrInvalid)
	}
	rawProfile, err := os.ReadFile(profilePath)
	if err != nil {
		return nil, fmt.Errorf("read Chromium seccomp profile: %w", err)
	}
	var profile bytes.Buffer
	if err := json.Compact(&profile, rawProfile); err != nil {
		return nil, fmt.Errorf("read Chromium seccomp profile: %w", err)
	}
	if err := (containers.Ownership{Environment: hostID, Project: "project", Resource: "browser", Generation: 1}).Validate(); err != nil {
		return nil, err
	}
	adapter, err := containers.New(ctx, docker)
	if err != nil {
		return nil, err
	}
	image, err := docker.ImageInspect(ctx, imageID)
	if err != nil {
		return nil, fmt.Errorf("inspect Browser Run image: %w", err)
	}
	if image.ID != imageID {
		return nil, fmt.Errorf("Browser Run image identity mismatch: %w", containers.ErrImageDigestMismatch)
	}
	reservations, err := capacity.NewStore(store.DB)
	if err != nil {
		return nil, err
	}
	runner, err := executions.NewService(reservations, adapter, hostID)
	if err != nil {
		return nil, err
	}
	screenshots, err := openBrowserArtifactStore(screenshotDirectory, maxScreenshotBytes)
	if err != nil {
		return nil, err
	}
	downloads, err := openBrowserArtifactStore(filepath.Join(filepath.Dir(screenshotDirectory), "browser-downloads"), maxDownloadBytes)
	if err != nil {
		return nil, errors.Join(err, screenshots.Close())
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	transport.MaxResponseHeaderBytes = 64 << 10
	return &Service{
		store: store, screenshots: screenshots, downloads: downloads, capacity: reservations, executions: runner, adapter: adapter,
		http:   &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		hostID: hostID, image: imageID, profile: profile.String(), key: bytes.Clone(key),
	}, nil
}

func (s *Service) Close() error { return errors.Join(s.screenshots.Close(), s.downloads.Close()) }

func (s *Service) Create(ctx context.Context, projectID uuid.UUID, lifetime time.Duration) (database.BrowserSession, error) {
	if lifetime == 0 {
		lifetime = defaultSessionLifetime
	}
	if lifetime < 5*time.Second || lifetime > defaultSessionLifetime {
		return database.BrowserSession{}, fmt.Errorf("browser session lifetime must be between 5 and 300 seconds: %w", fs.ErrInvalid)
	}
	defaults := capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}
	if err := s.capacity.EnsureDefaults(ctx, s.hostID, projectID, defaults); err != nil {
		return database.BrowserSession{}, err
	}
	if err := s.reapExpired(ctx); err != nil {
		return database.BrowserSession{}, err
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	session := database.BrowserSession{
		ID: uuid.New(), ProjectID: projectID, HostID: s.hostID, Status: "starting",
		CreatedAt: now, ExpiresAt: now.Add(lifetime),
	}
	startDone := make(chan struct{})
	s.starting.Store(session.ID, startDone)
	defer func() {
		close(startDone)
		s.starting.Delete(session.ID)
	}()
	if err := s.store.CreateBrowserSession(ctx, session); err != nil {
		return database.BrowserSession{}, err
	}
	spec := containers.Spec{
		Ownership: ownership(session), Image: s.image, User: "pwuser",
		Port: runtimePort, Access: containers.AccessPublished,
		Limits: containers.Limits{CPUMillis: 2000, MemoryBytes: 3 << 30, PIDs: 512, OpenFiles: 4096},
		Env: []string{
			"CLOUDLET_BROWSER_TOKEN=" + s.token(session.ID),
			"CLOUDLET_BROWSER_DEADLINE=" + session.ExpiresAt.Format(time.RFC3339Nano),
		},
		ChromiumSeccompProfile: s.profile,
	}
	startCtx, stop := context.WithDeadline(ctx, now.Add(min(45*time.Second, lifetime)))
	defer stop()
	execution, err := s.executions.Start(startCtx, executions.StartRequest{ExecutionID: session.ID, ProjectID: projectID, Spec: spec})
	if err == nil {
		_, err = s.adapter.WaitReady(startCtx, spec.Ownership, execution.Container.ID, containers.ReadinessProbe{
			Port: runtimePort, Path: "/readyz", Timeout: 30 * time.Second,
			Interval: 100 * time.Millisecond, Statuses: []int{http.StatusOK},
		})
	}
	if err == nil {
		err = s.store.SetBrowserSessionStatus(startCtx, session, "ready")
	}
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		_, cleanupErr := s.finish(cleanupCtx, session, "failed")
		return database.BrowserSession{}, errors.Join(err, cleanupErr)
	}
	session.Status = "ready"
	return session, nil
}

func (s *Service) Session(ctx context.Context, projectID, id uuid.UUID) (database.BrowserSession, error) {
	session, err := s.store.BrowserSession(ctx, projectID, id, s.hostID)
	if err != nil {
		return database.BrowserSession{}, err
	}
	if !terminalSession(session) && !time.Now().Before(session.ExpiresAt) {
		return s.closeSession(ctx, session)
	}
	return session, nil
}

func ownership(session database.BrowserSession) containers.Ownership {
	return containers.Ownership{Environment: session.HostID, Project: session.ProjectID.String(), Resource: "browser-" + session.ID.String(), Generation: 1}
}

func (s *Service) token(id uuid.UUID) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte("cloudlet-browser-session-v1\x00"))
	mac.Write(id[:])
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
