package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestLoadSecretKey(t *testing.T) {
	file := filepath.Join(t.TempDir(), "cloudlet.json")
	source := fmt.Sprintf(`{"environment":"development","instance":"default","public_origin":"http://localhost","api_address":"127.0.0.1:8080","gateway_address":"127.0.0.1:8081","site_storage_directory":%q}`, t.TempDir())
	if err := os.WriteFile(file, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLOUDLET_DATABASE_URL", "postgres://cloudlet:password@127.0.0.1:5432/cloudlet")
	t.Setenv("CLOUDLET_SECRET_KEY", strings.Repeat("ab", 32))
	cfg, err := config.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SecretKey) != 32 || cfg.SecretKey[0] != 0xab {
		t.Fatalf("secret key = %x", cfg.SecretKey)
	}
	t.Setenv("CLOUDLET_SECRET_KEY", "not-a-key")
	if _, err := config.Load(file); err == nil {
		t.Fatal("invalid secret key was accepted")
	}
}

func TestLoadBrowserSessionLifetime(t *testing.T) {
	file := filepath.Join("..", "..", "configs", "cloudlet.json")
	t.Setenv("CLOUDLET_DATABASE_URL", "")
	t.Setenv("CLOUDLET_BROWSER_IMAGE", "")
	t.Setenv("CLOUDLET_BROWSER_SECCOMP", "")
	for _, test := range []struct {
		value string
		want  time.Duration
		valid bool
	}{
		{"", 0, true},
		{"5", 5 * time.Second, true},
		{"300", 300 * time.Second, true},
		{"0", 0, false},
		{"4", 0, false},
		{"301", 0, false},
		{"1.5", 0, false},
		{"99999999999", 0, false},
	} {
		t.Setenv("CLOUDLET_BROWSER_SESSION_SECONDS", test.value)
		cfg, err := config.Load(file)
		if (err == nil) != test.valid || cfg.BrowserSessionLifetime != test.want {
			t.Errorf("lifetime %q = %s (%v), want %s, valid=%v", test.value, cfg.BrowserSessionLifetime, err, test.want, test.valid)
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
		Environment: "development", Instance: "test", DatabaseURL: "postgres://example", SecretKey: make([]byte, 32),
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
