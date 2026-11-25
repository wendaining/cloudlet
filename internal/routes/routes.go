package routes

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"sync"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/gateway"
)

var ErrStaticSiteNotReady = errors.New("static site version files are not ready")

var resourcePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// Route is one project resource route before persistence.
type Route struct {
	Resource   string
	Host       string
	PathPrefix string
	Target     string
}

// SyncError reports a committed route snapshot that the Gateway did not apply.
type SyncError struct {
	Version int64
	Err     error
}

func (e *SyncError) Error() string {
	return fmt.Sprintf("route snapshot version %d was committed but not applied: %v", e.Version, e.Err)
}

func (e *SyncError) Unwrap() error {
	return e.Err
}

// Publisher serializes project route publication and Gateway snapshot updates.
type Publisher struct {
	store          *database.Store
	gateway        *gateway.Handler
	openSite       func(database.StaticSiteVersion) (http.Handler, error)
	mu             sync.Mutex
	applied        bool
	appliedVersion int64
}

func NewPublisher(store *database.Store, handler *gateway.Handler) (*Publisher, error) {
	return NewPublisherWithSites(store, handler, nil)
}

// NewPublisherWithSites configures resolution of persisted static site routes.
func NewPublisherWithSites(store *database.Store, handler *gateway.Handler, openSite func(database.StaticSiteVersion) (http.Handler, error)) (*Publisher, error) {
	if store == nil || handler == nil {
		return nil, fmt.Errorf("create route publisher: database store and Gateway handler are required: %w", fs.ErrInvalid)
	}
	return &Publisher{store: store, gateway: handler, openSite: openSite}, nil
}

// ReservedHost reports whether startup routes own the host.
func (p *Publisher) ReservedHost(host string) bool {
	return p.gateway.ReservedHost(host)
}

// DrainProxy waits for requests already sent to a removed proxy endpoint.
func (p *Publisher) DrainProxy(ctx context.Context, host, target string) error {
	return p.gateway.DrainProxy(ctx, host, target)
}

// ActivateStaticSite publishes the first route for a resolved site version.
func (p *Publisher) ActivateStaticSite(ctx context.Context, projectID string, version database.StaticSiteVersion, hostname string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.openSite == nil {
		return fmt.Errorf("activate static site version %s: site handler is unavailable", version.ID)
	}
	if err := config.ValidateRouteHost(hostname); err != nil {
		return fmt.Errorf("activate static site: %w: %w", fs.ErrInvalid, err)
	}
	if len(hostname) > 253 || gateway.CanonicalHost(hostname) != hostname {
		return fmt.Errorf("activate static site: invalid canonical host %q: %w", hostname, fs.ErrInvalid)
	}
	if p.ReservedHost(hostname) {
		return fmt.Errorf("activate static site host %q: reserved by startup configuration: %w", hostname, database.ErrRouteConflict)
	}
	handler, err := p.openSite(version)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrInvalid) {
			err = fmt.Errorf("%w: %w", ErrStaticSiteNotReady, err)
		}
		return fmt.Errorf("verify files for static site version %s: %w", version.ID, err)
	}
	if handler == nil {
		return fmt.Errorf("verify files for static site version %s: missing handler", version.ID)
	}
	snapshot, err := p.store.ActivateStaticSiteVersion(ctx, projectID, version.SiteID, version.ID, hostname)
	if err != nil {
		return err
	}
	if _, err := p.syncLocked(ctx, snapshot); err != nil {
		return &SyncError{Version: snapshot, Err: err}
	}
	return nil
}

// PublishProject replaces one project's complete route set. A successful call
// leaves the committed snapshot applied to the Gateway.
func (p *Publisher) PublishProject(ctx context.Context, projectID string, routes []Route) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	records, err := normalizeRoutes(projectID, routes)
	if err != nil {
		return 0, err
	}
	for _, record := range records {
		if p.gateway.ReservedHost(record.Host) {
			return 0, fmt.Errorf("publish project %s route: host %q is reserved by startup configuration: %w", projectID, record.Host, fs.ErrInvalid)
		}
	}
	version, err := p.store.ReplaceProjectRoutes(ctx, projectID, records)
	if err != nil {
		return 0, err
	}
	appliedVersion, err := p.syncLocked(ctx, version)
	if err != nil {
		return version, &SyncError{Version: version, Err: err}
	}
	return appliedVersion, nil
}

// RemoveProject removes all routes owned by one project.
func (p *Publisher) RemoveProject(ctx context.Context, projectID string) (int64, error) {
	return p.PublishProject(ctx, projectID, nil)
}

// Sync reloads the committed route snapshot and applies it when it is newer
// than the last applied version.
func (p *Publisher) Sync(ctx context.Context) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.syncLocked(ctx, 0)
}

func (p *Publisher) syncLocked(ctx context.Context, minimumVersion int64) (int64, error) {
	version, records, err := p.store.RouteSnapshot(ctx)
	if err != nil {
		return 0, fmt.Errorf("load route snapshot: %w", err)
	}
	if version < minimumVersion {
		return 0, fmt.Errorf("route snapshot version %d is older than committed version %d", version, minimumVersion)
	}
	if p.applied && version == p.appliedVersion {
		return version, nil
	}
	proxies := make([]config.Route, 0, len(records))
	var sites []gateway.SiteRoute
	for _, record := range records {
		if record.StaticSiteVersionID != nil {
			if p.openSite == nil {
				return version, fmt.Errorf("restore static site route %q: site handler is unavailable", record.Host)
			}
			handler, err := p.openSite(database.StaticSiteVersion{ID: *record.StaticSiteVersionID, Directory: record.StaticSiteDirectory})
			if err != nil {
				return version, fmt.Errorf("restore static site version %s for host %q: %w", *record.StaticSiteVersionID, record.Host, err)
			}
			sites = append(sites, gateway.SiteRoute{Host: record.Host, Handler: handler})
			continue
		}
		proxies = append(proxies, config.Route{
			Host:       record.Host,
			PathPrefix: record.PathPrefix,
			Target:     record.Target,
		})
	}
	if err := p.gateway.ReplaceRoutesWithSites(proxies, sites); err != nil {
		return version, fmt.Errorf("apply route snapshot version %d: %w", version, err)
	}
	p.applied = true
	p.appliedVersion = version
	return version, nil
}

func normalizeRoutes(projectID string, routes []Route) ([]database.ProjectRoute, error) {
	if _, err := uuid.Parse(projectID); err != nil {
		return nil, fmt.Errorf("publish project routes: project ID %q is invalid: %w", projectID, fs.ErrInvalid)
	}
	records := make([]database.ProjectRoute, 0, len(routes))
	validated := make([]config.Route, 0, len(routes))
	for _, route := range routes {
		if !resourcePattern.MatchString(route.Resource) {
			return nil, fmt.Errorf("publish project %s route: resource %q must start with a letter or digit and contain at most 128 ASCII letters, digits, periods, underscores, colons or hyphens: %w", projectID, route.Resource, fs.ErrInvalid)
		}
		host := gateway.CanonicalHost(route.Host)
		prefix := route.PathPrefix
		if prefix == "" {
			prefix = "/"
		}
		validated = append(validated, config.Route{Host: host, PathPrefix: prefix, Target: route.Target})
		records = append(records, database.ProjectRoute{
			ProjectID:  projectID,
			Resource:   route.Resource,
			Host:       host,
			PathPrefix: prefix,
			Target:     route.Target,
		})
	}
	if err := config.ValidateRoutes(validated); err != nil {
		return nil, fmt.Errorf("publish project %s routes: %w: %v", projectID, fs.ErrInvalid, err)
	}
	return records, nil
}
