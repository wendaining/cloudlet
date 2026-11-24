package config

import (
	"cmp"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

type Route struct {
	Host       string `json:"host"`
	PathPrefix string `json:"path_prefix,omitempty"`
	Target     string `json:"target"`
}

type Config struct {
	Environment    string         `json:"environment,omitempty"`
	Instance       string         `json:"instance,omitempty"`
	PublicOrigin   string         `json:"public_origin,omitempty"`
	DatabaseURL    string         `json:"-"`
	SecretKey      []byte         `json:"-"`
	APIAddress     string         `json:"api_address"`
	GatewayAddress string         `json:"gateway_address"`
	Routes         []Route        `json:"routes"`
	Workers        *WorkerRuntime `json:"workers,omitempty"`
}

type ResourceBudget struct {
	CPUMillis   int64 `json:"cpu_millis"`
	MemoryBytes int64 `json:"memory_bytes"`
	Instances   int64 `json:"instances"`
}

// WorkerRuntime explicitly configures runtime storage and admission budgets.
type WorkerRuntime struct {
	Directory    string         `json:"directory"`
	HostID       string         `json:"host_id"`
	HostBudget   ResourceBudget `json:"host_budget"`
	ProjectQuota ResourceBudget `json:"project_quota"`
}

func (w WorkerRuntime) Validate() error {
	if !filepath.IsAbs(w.Directory) || w.HostID == "" || len(w.HostID) > 64 || strings.TrimSpace(w.HostID) != w.HostID {
		return errors.New("Worker runtime requires an absolute directory and a host identifier of 1 to 64 bytes")
	}
	for _, budget := range []ResourceBudget{w.HostBudget, w.ProjectQuota} {
		if budget.CPUMillis < 1000 || budget.MemoryBytes < 256<<20 || budget.Instances < 1 {
			return errors.New("Worker budgets must provide at least 1000 CPU millicores, 256 MiB memory and one instance")
		}
	}
	return nil
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode configuration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("configuration must contain one JSON object")
	}
	cfg.DatabaseURL = os.Getenv("CLOUDLET_DATABASE_URL")
	if value := os.Getenv("CLOUDLET_SECRET_KEY"); value != "" {
		key, err := hex.DecodeString(value)
		if err != nil || len(key) != 32 {
			return Config{}, errors.New("CLOUDLET_SECRET_KEY must contain 64 hexadecimal characters")
		}
		cfg.SecretKey = key
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg Config) Validate() error {
	if cfg.Workers != nil {
		if cfg.DatabaseURL == "" || cfg.Instance == "" {
			return errors.New("Worker runtime requires a management database and instance")
		}
		if err := cfg.Workers.Validate(); err != nil {
			return err
		}
	}
	if cfg.Environment != "" && cfg.Environment != "development" && cfg.Environment != "production" {
		return errors.New("invalid environment")
	}
	if cfg.Environment != "" {
		if cfg.DatabaseURL == "" || cfg.Instance == "" {
			return errors.New("management environment requires database URL and instance")
		}
		if len(cfg.SecretKey) != 32 {
			return errors.New("management environment requires a 32-byte secret key")
		}
		origin, err := url.Parse(cfg.PublicOrigin)
		if err != nil || origin.Hostname() == "" || (origin.Scheme != "http" && origin.Scheme != "https") {
			return errors.New("invalid public origin")
		}
	}
	for _, address := range []string{cfg.APIAddress, cfg.GatewayAddress} {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host == "" {
			return fmt.Errorf("invalid listen address: %q", address)
		}
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return fmt.Errorf("invalid listen port: %q", address)
		}
	}
	return ValidateRoutes(cfg.Routes)
}

// ValidateRoutes checks the host, path prefix and upstream rules shared by static and dynamic routes.
func ValidateRoutes(routes []Route) error {
	type routeKey struct{ host, prefix string }
	seen := make(map[routeKey]bool)
	for _, route := range routes {
		host, err := url.Parse("http://" + route.Host)
		if err != nil || route.Host == "" || host.Host != route.Host || host.Hostname() != route.Host || host.User != nil || host.Path != "" || host.RawQuery != "" || host.Fragment != "" {
			return fmt.Errorf("route host must be a hostname without port: %q", route.Host)
		}
		prefix := cmp.Or(route.PathPrefix, "/")
		if !strings.HasPrefix(prefix, "/") || path.Clean(prefix) != prefix || strings.ContainsAny(prefix, "%?#\\") || strings.ContainsFunc(prefix, unicode.IsControl) {
			return fmt.Errorf("route path prefix must be a clean absolute path: %q", route.PathPrefix)
		}
		key := routeKey{strings.ToLower(strings.TrimSuffix(route.Host, ".")), prefix}
		if seen[key] {
			return fmt.Errorf("duplicate route for host %q and path prefix %q", route.Host, prefix)
		}
		seen[key] = true
		target, err := url.Parse(route.Target)
		if err != nil || target.Hostname() == "" || (target.Scheme != "http" && target.Scheme != "https") || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
			return fmt.Errorf("invalid HTTP route target: %q", route.Target)
		}
		if port := target.Port(); port != "" || strings.HasSuffix(target.Host, ":") {
			number, err := strconv.ParseUint(port, 10, 16)
			if err != nil {
				return fmt.Errorf("invalid HTTP route target port in %q: %w", route.Target, err)
			}
			if number == 0 {
				return fmt.Errorf("invalid HTTP route target port in %q: port must be between 1 and 65535", route.Target)
			}
		}
	}
	return nil
}
