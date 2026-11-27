//go:build integration

package database_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func verifyBrowserAcceptanceExample(t *testing.T, ctx context.Context, apiOrigin, project, fixture, email, password string) {
	t.Helper()
	api, err := url.Parse(apiOrigin)
	if err != nil {
		t.Fatal(err)
	}
	localFixture, err := url.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("browser acceptance example requires Node.js: %v", err)
	}
	command := exec.CommandContext(ctx, node, "../../scripts/browser-acceptance.mjs", project, localFixture.Hostname())
	temp := t.TempDir()
	command.Env = append(os.Environ(),
		"TMPDIR="+temp, "CLOUDLET_API_PORT="+api.Port(), "CLOUDLET_DEV_EMAIL="+email, "CLOUDLET_DEV_PASSWORD="+password)
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		t.Fatalf("run browser acceptance example: %v\n%s", err, stderr.String())
	}
	var result struct {
		Directory string `json:"directory"`
		Digest    string `json:"sha256"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("parse browser example output: %v (%s)", err, output)
	}
	if filepath.Dir(result.Directory) != temp {
		t.Fatalf("browser example wrote outside the test output directory: %s", result.Directory)
	}
	file, err := os.Open(filepath.Join(result.Directory, "screenshot.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	config, err := png.DecodeConfig(file)
	if err != nil || config.Height <= 720 || config.Width != 1280 {
		t.Fatalf("example did not save a full-page PNG: %+v (%v)", config, err)
	}
	screenshot, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256(screenshot)); digest != result.Digest {
		t.Fatalf("stored screenshot digest = %s, want %s", digest, result.Digest)
	}
	log, err := os.ReadFile(filepath.Join(result.Directory, "console.json"))
	if err != nil || !strings.Contains(string(log), "Cloudlet Browser acceptance click") {
		t.Fatalf("example output does not include the console marker: %v (%s)", err, log)
	}
	t.Logf("replayed the documented example against Cloudlet using real Chromium; full-page PNG %d×%d, SHA-256 verified, console marker captured", config.Width, config.Height)
}
