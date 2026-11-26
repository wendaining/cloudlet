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

	"github.com/wendaining/cloudlet/internal/durableobjects"
)

const (
	RuntimeVersion = "1.20261012.1"
	RuntimeImage   = "ghcr.io/ryanhecht/workerd:" + RuntimeVersion + "@sha256:d540e8dc2614e35a6627ff8b34fed14c61a00eecd1256f6ac5c3f17c901d3c47"

	ConfigPath = "/worker/config.capnp"
	ModulePath = "/worker/worker.mjs"
	HTTPPort   = 8080
)

var maxCompatibilityDate = time.Date(2026, time.October, 19, 0, 0, 0, 0, time.UTC)

var entryPathPattern = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)

// RuntimeConfig contains the files to place at ConfigPath and ModulePath and
// the metadata required by runtime preflight.
type RuntimeConfig struct {
	Config             []byte
	Module             []byte
	Entry              string
	CompatibilityDate  string
	CompatibilityFlags []string
	DurableClasses     []string
}

// ServeArgs returns the arguments for the pinned image's entrypoint.
func ServeArgs() []string {
	return []string{"serve", ConfigPath}
}

// Prepare reads one bundled JavaScript ES module from bundleDir. The entry is
// a relative .js or .mjs path with ASCII letters, digits, periods, underscores,
// hyphens and slash separators.
func Prepare(bundleDir, entry, compatibilityDate string) (RuntimeConfig, error) {
	return prepare(bundleDir, entry, compatibilityDate, nil)
}

// PrepareWithDurableObjects configures SQLite namespaces in the same module.
// The caller must mount an exclusive writable volume at durableobjects.StoragePath.
func PrepareWithDurableObjects(bundleDir, entry, compatibilityDate string, namespaces []durableobjects.Namespace) (RuntimeConfig, error) {
	if err := durableobjects.Validate(namespaces); err != nil {
		return RuntimeConfig{}, fmt.Errorf("configure Durable Objects for %q: %w", entry, err)
	}
	return prepare(bundleDir, entry, compatibilityDate, namespaces)
}

func prepare(bundleDir, entry, compatibilityDate string, namespaces []durableobjects.Namespace) (RuntimeConfig, error) {
	ext := path.Ext(entry)
	if !fs.ValidPath(entry) || !entryPathPattern.MatchString(entry) || (ext != ".js" && ext != ".mjs") {
		return RuntimeConfig{}, fmt.Errorf("entry %q must be a relative .js or .mjs path with supported characters", entry)
	}
	if err := validateCompatibilityDate(compatibilityDate, time.Now()); err != nil {
		return RuntimeConfig{}, err
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
	return prepareModule(entry, module, compatibilityDate, namespaces, nil, durableobjects.StoragePath)
}

// PrepareModule prepares a bundled ES module already read from an upload.
func PrepareModule(module []byte, compatibilityDate string) (RuntimeConfig, error) {
	return prepareModule("index.mjs", module, compatibilityDate, nil, nil, "")
}

// PrepareModuleWithFlags prepares an uploaded module with explicit compatibility flags.
func PrepareModuleWithFlags(module []byte, compatibilityDate string, flags []string) (RuntimeConfig, error) {
	return prepareModule("index.mjs", module, compatibilityDate, nil, flags, "")
}

// PrepareDurableModule prepares an uploaded module for a managed SQLite volume.
func PrepareDurableModule(module []byte, compatibilityDate string, namespaces []durableobjects.Namespace) (RuntimeConfig, error) {
	return PrepareDurableModuleWithFlags(module, compatibilityDate, namespaces, nil)
}

// PrepareDurableModuleWithFlags prepares a Durable Object module with explicit compatibility flags.
func PrepareDurableModuleWithFlags(module []byte, compatibilityDate string, namespaces []durableobjects.Namespace, flags []string) (RuntimeConfig, error) {
	if err := durableobjects.Validate(namespaces); err != nil {
		return RuntimeConfig{}, err
	}
	return prepareModule("index.mjs", module, compatibilityDate, namespaces, flags, path.Join(durableobjects.StoragePath, durableobjects.StorageDirectory))
}

func prepareModule(entry string, module []byte, compatibilityDate string, namespaces []durableobjects.Namespace, flags []string, storagePath string) (RuntimeConfig, error) {
	if err := validateCompatibilityDate(compatibilityDate, time.Now()); err != nil {
		return RuntimeConfig{}, err
	}
	if !utf8.Valid(module) || len(bytes.TrimSpace(module)) == 0 {
		return RuntimeConfig{}, fmt.Errorf("entry %q must contain non-empty UTF-8 JavaScript", entry)
	}

	normalized, err := normalizeCompatibilityFlags(flags)
	if err != nil {
		return RuntimeConfig{}, err
	}

	var classes []string
	for _, namespace := range namespaces {
		classes = append(classes, namespace.ClassName)
	}
	return RuntimeConfig{
		Config:             renderConfig(entry, compatibilityDate, namespaces, normalized, storagePath),
		Module:             bytes.Clone(module),
		Entry:              entry,
		CompatibilityDate:  compatibilityDate,
		CompatibilityFlags: normalized,
		DurableClasses:     classes,
	}, nil
}

func validateCompatibilityDate(compatibilityDate string, now time.Time) error {
	date, err := time.Parse(time.DateOnly, compatibilityDate)
	if err != nil {
		return fmt.Errorf("compatibility date %q must be a calendar date in YYYY-MM-DD format: %w", compatibilityDate, err)
	}
	if date.Year() < 2000 || date.After(maxCompatibilityDate) {
		return fmt.Errorf("compatibility date %q is outside the range 2000-01-01 through %s for workerd %s", compatibilityDate, maxCompatibilityDate.Format(time.DateOnly), RuntimeVersion)
	}
	if date.After(now) {
		return fmt.Errorf("compatibility date %q is in the future (UTC)", compatibilityDate)
	}
	return nil
}

func renderConfig(entry, compatibilityDate string, namespaces []durableobjects.Namespace, flags []string, storagePath string) []byte {
	var storageService, workerFields []byte
	if len(namespaces) > 0 {
		storageService = fmt.Appendf(nil, `, (name = "durable-storage", disk = (path = "%s", writable = true))`, storagePath)
		workerFields = append(workerFields, "  durableObjectStorage = (localDisk = \"durable-storage\"),\n  durableObjectNamespaces = [\n"...)
		for _, namespace := range namespaces {
			workerFields = fmt.Appendf(workerFields, "    (className = \"%s\", uniqueKey = \"%s\", enableSql = true),\n", namespace.ClassName, namespace.ID)
		}
		workerFields = append(workerFields, "  ],\n  bindings = [\n"...)
		for _, namespace := range namespaces {
			for _, binding := range namespace.Bindings {
				workerFields = fmt.Appendf(workerFields, "    (name = \"%s\", durableObjectNamespace = \"%s\"),\n", binding, namespace.ClassName)
			}
		}
		workerFields = append(workerFields, "  ],\n"...)
	}
	return fmt.Appendf(nil, configTemplate, storageService, HTTPPort, entry, path.Base(ModulePath), compatibilityDate, renderCompatibilityFlags(flags), workerFields)
}

const configTemplate = `using Workerd = import "/workerd/workerd.capnp";

const config :Workerd.Config = (
  services = [(name = "worker", worker = .worker)%s],
  sockets = [(name = "http", address = "*:%d", http = (), service = "worker")],
);

const worker :Workerd.Worker = (
  modules = [(name = "%s", esModule = embed "%s")],
  compatibilityDate = "%s",
%s%s);
`
