package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wendaining/cloudlet/internal/config"
)

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	for name, source := range map[string]string{
		"unknown field":  `{"api_address":"127.0.0.1:8080","gateway_address":"127.0.0.1:8081","typo":true}`,
		"extra object":   `{"api_address":"127.0.0.1:8080","gateway_address":"127.0.0.1:8081"} {}`,
		"invalid port":   `{"api_address":"127.0.0.1:99999","gateway_address":"127.0.0.1:8081"}`,
		"duplicate":      `{"api_address":"127.0.0.1:8080","gateway_address":"127.0.0.1:8081","routes":[{"host":"app.localhost","target":"http://127.0.0.1:9000"},{"host":"APP.localhost.","target":"http://127.0.0.1:9001"}]}`,
		"invalid target": `{"api_address":"127.0.0.1:8080","gateway_address":"127.0.0.1:8081","routes":[{"host":"app.localhost","target":"file:///etc/passwd"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "cloudlet.json")
			if err := os.WriteFile(file, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(file); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}

func TestRepositoryConfigurations(t *testing.T) {
	for _, file := range []string{"cloudlet.json", "container.json"} {
		if _, err := config.Load(filepath.Join("..", "..", "configs", file)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestValidateRoutePathPrefixes(t *testing.T) {
	defaultRoute := config.Route{Host: "app.localhost", Target: "http://localhost"}
	for _, test := range []struct {
		prefix string
		valid  bool
	}{
		{prefix: "/api", valid: true},
		{prefix: "/api/中文", valid: true},
		{prefix: "/"}, // Same route as the default prefix.
		{prefix: "api"},
		{prefix: "/api/"},
		{prefix: "/api//v1"},
		{prefix: "/api/../v1"},
		{prefix: "/api?mode=1"},
		{prefix: "/api#fragment"},
		{prefix: "/api%2Fv1"},
	} {
		t.Run(test.prefix, func(t *testing.T) {
			route := defaultRoute
			route.Host = "APP.localhost."
			route.PathPrefix = test.prefix
			err := config.ValidateRoutes([]config.Route{defaultRoute, route})
			if (err == nil) != test.valid {
				t.Fatalf("valid = %v, validation returned %v", test.valid, err)
			}
		})
	}
}

func TestValidateRoutesUpstreamPort(t *testing.T) {
	for _, test := range []struct {
		target string
		valid  bool
	}{
		{target: "http://localhost", valid: true},
		{target: "http://localhost:1", valid: true},
		{target: "https://[::1]:65535", valid: true},
		{target: "http://[::1]", valid: true},
		{target: "http://localhost:0"},
		{target: "http://localhost:65536"},
		{target: "http://[::1]:"},
	} {
		t.Run(test.target, func(t *testing.T) {
			err := config.ValidateRoutes([]config.Route{{Host: "app.localhost", Target: test.target}})
			if (err == nil) != test.valid {
				t.Fatalf("valid = %v, validation returned %v", test.valid, err)
			}
		})
	}
}

func TestManagementRequiresAbsoluteSiteStorageDirectory(t *testing.T) {
	cfg := config.Config{
		Environment: "development", Instance: "test", DatabaseURL: "postgres://example",
		PublicOrigin: "http://localhost", APIAddress: "127.0.0.1:8080", GatewayAddress: "127.0.0.1:8081",
	}
	for _, directory := range []string{"", "relative/storage"} {
		cfg.SiteStorageDirectory = directory
		if err := cfg.Validate(); err == nil {
			t.Fatalf("accepted storage directory %q", directory)
		}
	}
	cfg.SiteStorageDirectory = t.TempDir()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
