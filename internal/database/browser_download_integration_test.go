//go:build integration

package database_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/database"
)

func verifyBrowserDownloads(t *testing.T, ctx context.Context, store *database.Store, owner, other *apigen.ClientWithResponses, project string, id uuid.UUID, fixture string) {
	t.Helper()
	original, err := url.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(original.Hostname(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	source := []byte("actual browser download\x00\x01\x02")
	cancelled := make(chan struct{}, 1)
	server := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/small":
				w.Header().Set("Content-Disposition", `attachment; filename="sample.bin"`)
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Write(source)
			case "/large":
				w.Header().Set("Content-Disposition", `attachment; filename="large.bin"`)
				w.Header().Set("Content-Type", "application/octet-stream")
				buffer := bytes.Repeat([]byte("x"), 64<<10)
				flusher := http.NewResponseController(w)
				for range 256 {
					_, err := w.Write(buffer)
					if err == nil {
						err = flusher.Flush()
					}
					if err != nil {
						select {
						case cancelled <- struct{}{}:
						default:
						}
						return
					}
					time.Sleep(6 * time.Millisecond)
				}
			default:
				fmt.Fprint(w, `<!doctype html><title>Download fixture</title><a id="small" href="/small" download>Small</a><a id="large" href="/large" download>Large</a>`)
			}
		}),
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	page, err := owner.NavigateBrowserSessionWithResponse(ctx, project, id, apigen.NavigateBrowserSessionJSONRequestBody{Url: "http://" + listener.Addr().String() + "/"})
	if err != nil || page.StatusCode() != http.StatusOK {
		t.Fatalf("navigate download fixture: %v (%v)", page, err)
	}
	capture := func(selector string, want int) *apigen.BrowserDownload {
		t.Helper()
		result, err := owner.CaptureBrowserDownloadWithResponse(ctx, project, id, apigen.CaptureBrowserDownloadJSONRequestBody{Selector: selector})
		if err != nil || result.StatusCode() != want {
			t.Fatalf("capture %q: %v (%v), want HTTP %d", selector, result, err, want)
		}
		return result.JSON201
	}
	get := func(api *apigen.ClientWithResponses, downloadID uuid.UUID, want int) []byte {
		t.Helper()
		result, err := api.GetBrowserDownloadWithResponse(ctx, project, id, downloadID)
		if err != nil || result.StatusCode() != want {
			t.Fatalf("read browser download: %v (%v), want HTTP %d", result, err, want)
		}
		if want == http.StatusOK && !strings.Contains(result.HTTPResponse.Header.Get("Content-Disposition"), "sample.bin") {
			t.Fatalf("missing attachment name: %v", result.HTTPResponse.Header)
		}
		return result.Body
	}
	small := capture("#small", http.StatusCreated)
	if small == nil || small.Filename != "sample.bin" || small.SizeBytes != int64(len(source)) ||
		small.ArtifactDigest != fmt.Sprintf("%x", sha256.Sum256(source)) {
		t.Fatalf("incorrect download metadata: %+v", small)
	}
	if result := get(owner, small.Id, http.StatusOK); !bytes.Equal(source, result) {
		t.Fatal("stored artifact differs from the real download")
	}
	get(other, small.Id, http.StatusNotFound)

	capture("#large", http.StatusUnprocessableEntity)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Chromium did not cancel the oversized transfer before it finished")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var records int64
	if err := store.DB.Model(&database.BrowserDownload{}).Where("session_id = ?", id).Count(&records).Error; err != nil || records != 1 {
		t.Fatalf("oversized transfer left a download record: %d (%v)", records, err)
	}
	reservations, err := capacity.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := reservations.GetReservation(ctx, uuid.MustParse(project), id)
	if err != nil {
		t.Fatal(err)
	}
	checkFiles := exec.CommandContext(ctx, "docker", "exec", reservation.ContainerID, "sh", "-c",
		"find /tmp/cloudlet-downloads-* -maxdepth 1 -type f -print")
	if output, err := checkFiles.CombinedOutput(); err != nil || len(bytes.TrimSpace(output)) != 0 {
		t.Fatalf("browser download temporary files were not cleared: %s (%v)", output, err)
	}
	capture("#small", http.StatusCreated)
	t.Log("real browser download bytes and SHA-256 match, cross-project returned 404; oversize chunked transfer was cancelled and not registered; a later download succeeded")
}
