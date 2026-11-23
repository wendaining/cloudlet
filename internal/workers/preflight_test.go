package workers

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
)

func TestPreflightConfigIncludesWrapperAndUserModule(t *testing.T) {
	config := string(preflightConfig("2026-10-12"))
	for _, expected := range []string{
		`name = "wrapper.mjs", esModule = embed "wrapper.mjs"`,
		`name = "user.mjs", esModule = embed "user.mjs"`,
		`compatibilityDate = "2026-10-12"`,
	} {
		if !strings.Contains(config, expected) {
			t.Errorf("preflight config does not contain %q:\n%s", expected, config)
		}
	}
}

func TestPreflightArchiveWritesReadOnlyWorkerFiles(t *testing.T) {
	archive, err := preflightArchive(map[string][]byte{
		"config.capnp": []byte("config"),
		"wrapper.mjs":  []byte("wrapper"),
		"user.mjs":     []byte("user"),
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	files := map[string]string{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Mode != 0o444 {
			t.Errorf("%s mode = %o, want 444", header.Name, header.Mode)
		}
		files[header.Name] = string(content)
	}
	for name, content := range map[string]string{
		"worker/config.capnp": "config",
		"worker/wrapper.mjs":  "wrapper",
		"worker/user.mjs":     "user",
	} {
		if files[name] != content {
			t.Errorf("%s = %q, want %q", name, files[name], content)
		}
	}
}

func TestClassifyPreflight(t *testing.T) {
	for _, tt := range []struct {
		name   string
		logs   string
		status int64
		want   PreflightPhase
	}{
		{"ready", "cloudlet-preflight:loading\ncloudlet-preflight:ready\n", 0, PreflightReady},
		{"initialize", "cloudlet-preflight:loading\ncloudlet-preflight:initialize:Error: boom\n", 1, PreflightInitialize},
		{"ready then failed", "cloudlet-preflight:ready\n", 1, PreflightInitialize},
		{"syntax", "service worker: Uncaught SyntaxError: Unexpected token '}'\n", 1, PreflightLoad},
		{"missing module", "error: /worker/config.capnp: No such file.\n", 1, PreflightLoad},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := classifyPreflight(tt.logs, tt.status)
			if result.Phase != tt.want {
				t.Fatalf("phase = %s, want %s", result.Phase, tt.want)
			}
		})
	}
}

func TestTruncatePreflightLogKeepsTail(t *testing.T) {
	logs := strings.Repeat("a", maxPreflightLog) + "tail"
	truncated := truncatePreflightLog(logs)
	if !strings.HasPrefix(truncated, "[earlier output truncated]\n") || !strings.HasSuffix(truncated, "tail") {
		t.Fatalf("truncated log = %q", truncated)
	}
	if len(truncated) > maxPreflightLog+len("[earlier output truncated]\n") {
		t.Fatalf("truncated log length = %d", len(truncated))
	}
}

func TestClassifyPreflightKeepsMarkersBeforeTruncation(t *testing.T) {
	logs := "cloudlet-preflight:ready\n" + strings.Repeat("a", maxPreflightLog+1)
	result := classifyPreflight(logs, 0)
	if result.Phase != PreflightReady {
		t.Fatalf("phase = %s, want %s", result.Phase, PreflightReady)
	}
	if !strings.HasPrefix(result.Logs, "[earlier output truncated]\n") {
		t.Fatalf("logs were not truncated: %q", result.Logs)
	}
}

func TestPreflightRejectsMissingInputs(t *testing.T) {
	if _, err := Preflight(context.Background(), nil, RuntimeConfig{}); err == nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("Preflight error = %v, want invalid input", err)
	}
}
