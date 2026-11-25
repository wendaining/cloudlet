package workerpublish_test

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/wendaining/cloudlet/internal/workerpublish"
)

func TestValidatePublicationRequest(t *testing.T) {
	valid := workerpublish.Request{Module: `export default { fetch() { return new Response("ready") } };`, CompatibilityDate: "2026-10-12", Hostname: "worker.localhost"}
	for _, test := range []struct {
		name   string
		change func(*workerpublish.Request)
		worker string
		key    string
	}{
		{name: "empty module", change: func(r *workerpublish.Request) { r.Module = " " }},
		{name: "invalid UTF-8", change: func(r *workerpublish.Request) { r.Module = "\xff" }},
		{name: "oversized module", change: func(r *workerpublish.Request) { r.Module = strings.Repeat("x", workerpublish.MaxModuleBytes+1) }},
		{name: "future compatibility", change: func(r *workerpublish.Request) { r.CompatibilityDate = "2099-01-01" }},
		{name: "invalid date", change: func(r *workerpublish.Request) { r.CompatibilityDate = "2026-02-30" }},
		{name: "host with port", change: func(r *workerpublish.Request) { r.Hostname = "worker.localhost:8080" }},
		{name: "host with path", change: func(r *workerpublish.Request) { r.Hostname = "worker.localhost/path" }},
		{name: "empty host", change: func(r *workerpublish.Request) { r.Hostname = "" }},
		{name: "invalid name", worker: "../worker"},
		{name: "long name", worker: strings.Repeat("a", 64)},
		{name: "long key", key: strings.Repeat("a", 257)},
		{name: "blank key", key: " "},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, name, key := valid, "worker", "publication-1"
			if test.change != nil {
				test.change(&request)
			}
			if test.worker != "" {
				name = test.worker
			}
			if test.key != "" {
				key = test.key
			}
			if _, err := workerpublish.ValidateRequest(name, key, request); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("validation returned %v", err)
			}
		})
	}
	valid.Module = strings.Repeat("x", workerpublish.MaxModuleBytes)
	prepared, err := workerpublish.ValidateRequest("worker", "publication", valid)
	if err != nil || len(prepared.Module) != workerpublish.MaxModuleBytes {
		t.Fatalf("module byte limit: %v", err)
	}
}
