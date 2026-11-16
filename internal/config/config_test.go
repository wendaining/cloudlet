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
