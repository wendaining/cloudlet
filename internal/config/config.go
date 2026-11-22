package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Route struct {
	Host   string `json:"host"`
	Target string `json:"target"`
}

type Config struct {
	Environment    string  `json:"environment,omitempty"`
	Instance       string  `json:"instance,omitempty"`
	PublicOrigin   string  `json:"public_origin,omitempty"`
	DatabaseURL    string  `json:"-"`
	APIAddress     string  `json:"api_address"`
	GatewayAddress string  `json:"gateway_address"`
	Routes         []Route `json:"routes"`
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
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg Config) Validate() error {
	if cfg.Environment != "" && cfg.Environment != "development" && cfg.Environment != "production" {
		return errors.New("invalid environment")
	}
	if cfg.Environment != "" {
		if cfg.DatabaseURL == "" || cfg.Instance == "" {
			return errors.New("management environment requires database URL and instance")
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

// ValidateRoutes checks the host and upstream rules shared by static and dynamic routes.
func ValidateRoutes(routes []Route) error {
	seen := make(map[string]bool)
	for _, route := range routes {
		host, err := url.Parse("http://" + route.Host)
		if err != nil || route.Host == "" || host.Host != route.Host || host.Hostname() != route.Host || host.User != nil || host.Path != "" || host.RawQuery != "" || host.Fragment != "" {
			return fmt.Errorf("route host must be a hostname without port: %q", route.Host)
		}
		key := strings.ToLower(strings.TrimSuffix(route.Host, "."))
		if seen[key] {
			return fmt.Errorf("duplicate route host: %q", route.Host)
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
