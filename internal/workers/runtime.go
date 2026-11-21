package workers

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"time"
	"unicode/utf8"
)

const (
	RuntimeVersion = "1.20261012.1"
	RuntimeImage   = "ghcr.io/ryanhecht/workerd:" + RuntimeVersion + "@sha256:d540e8dc2614e35a6627ff8b34fed14c61a00eecd1256f6ac5c3f17c901d3c47"

	ConfigPath = "/worker/config.capnp"
	ModulePath = "/worker/worker.mjs"
	HTTPPort   = 8080

	maxCompatibilityDate = "2026-10-19"
)

var entryPathPattern = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

// RuntimeConfig contains the files to place at ConfigPath and ModulePath.
type RuntimeConfig struct {
	Config []byte
	Module []byte
}

// ServeArgs returns the arguments for the pinned image's entrypoint.
func ServeArgs() []string {
	return []string{"serve", ConfigPath}
}

// Prepare reads one bundled JavaScript ES module from bundleDir. The entry is
// a relative .js or .mjs path with ASCII letters, digits, periods, underscores,
// hyphens and slash separators.
func Prepare(bundleDir, entry, compatibilityDate string) (RuntimeConfig, error) {
	ext := path.Ext(entry)
	if !fs.ValidPath(entry) || !entryPathPattern.MatchString(entry) || (ext != ".js" && ext != ".mjs") {
		return RuntimeConfig{}, fmt.Errorf("entry %q must be a relative .js or .mjs path with supported characters", entry)
	}
	date, err := time.Parse(time.DateOnly, compatibilityDate)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("compatibility date %q must be a calendar date in YYYY-MM-DD format: %w", compatibilityDate, err)
	}
	if date.Year() < 2000 || compatibilityDate > maxCompatibilityDate {
		return RuntimeConfig{}, fmt.Errorf("compatibility date %q is outside the range 2000-01-01 through %s for workerd %s", compatibilityDate, maxCompatibilityDate, RuntimeVersion)
	}
	if compatibilityDate > time.Now().UTC().Format(time.DateOnly) {
		return RuntimeConfig{}, fmt.Errorf("compatibility date %q is in the future (UTC)", compatibilityDate)
	}

	root, err := os.OpenRoot(bundleDir)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("open Worker bundle %q: %w", bundleDir, err)
	}
	defer root.Close()
	info, err := root.Stat(entry)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("inspect entry %q in bundle %q: %w", entry, bundleDir, err)
	}
	if !info.Mode().IsRegular() {
		return RuntimeConfig{}, fmt.Errorf("entry %q in bundle %q must be a regular file", entry, bundleDir)
	}
	module, err := root.ReadFile(entry)
	if err != nil {
		return RuntimeConfig{}, fmt.Errorf("read entry %q in bundle %q: %w", entry, bundleDir, err)
	}
	if !utf8.Valid(module) || len(bytes.TrimSpace(module)) == 0 {
		return RuntimeConfig{}, fmt.Errorf("entry %q in bundle %q must contain non-empty UTF-8 JavaScript", entry, bundleDir)
	}

	return RuntimeConfig{
		Config: fmt.Appendf(nil, configTemplate, HTTPPort, entry, path.Base(ModulePath), compatibilityDate),
		Module: module,
	}, nil
}

const configTemplate = `using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [(name = "worker", worker = .worker)],
  sockets = [(name = "http", address = "*:%d", http = (), service = "worker")],
);

const worker :Workerd.Worker = (
  modules = [(name = "%s", esModule = embed "%s")],
  compatibilityDate = "%s",
);
`
