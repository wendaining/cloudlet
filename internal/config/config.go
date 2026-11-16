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
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (cfg Config) Validate() error {
	for _, address := range []string{cfg.APIAddress, cfg.GatewayAddress} {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host == "" {
			return fmt.Errorf("invalid listen address: %q", address)
		}
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return fmt.Errorf("invalid listen port: %q", address)
		}
	}
	seen := make(map[string]bool)
	for _, route := range cfg.Routes {
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
	}
	return nil
}
