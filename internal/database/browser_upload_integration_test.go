//go:build integration

package database_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
)

func verifyBrowserUpload(t *testing.T, ctx context.Context, owner *apigen.ClientWithResponses, project string,
	sessionID, sourceID uuid.UUID, artifact *apigen.BrowserScreenshot, downloaded *apigen.BrowserDownload, fixture string,
) {
	t.Helper()
	gateway, err := url.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(gateway.Hostname(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	type receipt struct{ filename, digest string }
	received := make(chan receipt, 1)
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `<!doctype html><title>Browser Upload</title>
				<form method="post" enctype="multipart/form-data">
				<input type="file" id="file" name="image" required>
				<input type="file"><button id="submit">Send</button></form>`)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 9<<20)
		file, header, err := r.FormFile("image")
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		if err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		defer file.Close()
		digest := sha256.New()
		if _, err := io.Copy(digest, file); err != nil {
			http.Error(w, "read file", http.StatusBadRequest)
			return
		}
		select {
		case received <- receipt{header.Filename, fmt.Sprintf("%x", digest.Sum(nil))}:
		default:
		}
		fmt.Fprint(w, "<title>Uploaded</title>")
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	navigation, err := owner.NavigateBrowserSessionWithResponse(ctx, project, sessionID, apigen.NavigateBrowserSessionJSONRequestBody{
		Url: "http://" + listener.Addr().String() + "/",
	})
	if err != nil || navigation.StatusCode() != http.StatusOK {
		t.Fatalf("navigate to upload form: %v (%v)", navigation, err)
	}
	timeout := 250
	input := apigen.AttachBrowserArtifactJSONRequestBody{
		ArtifactKind: "screenshot", SourceSessionId: sourceID, ArtifactId: artifact.Id, TimeoutMs: &timeout,
	}
	attach := func(selector string, want int) {
		t.Helper()
		input.Selector = selector
		result, err := owner.AttachBrowserArtifactWithResponse(ctx, project, sessionID, input)
		if err != nil || result.StatusCode() != want {
			t.Fatalf("attach %q: %v (%v), want HTTP %d", selector, result, err, want)
		}
	}
	attach("input", http.StatusUnprocessableEntity)
	attach("#missing", http.StatusGatewayTimeout)
	attach("#submit", http.StatusUnprocessableEntity)
	timeout = 3000
	for _, source := range []struct {
		kind, filename, digest string
		id                     uuid.UUID
	}{
		{"screenshot", "screenshot.png", artifact.ArtifactDigest, artifact.Id},
		{"download", downloaded.Filename, downloaded.ArtifactDigest, downloaded.Id},
	} {
		input.ArtifactKind = apigen.AttachBrowserArtifactRequestArtifactKind(source.kind)
		input.ArtifactId = source.id
		attach("#file", http.StatusNoContent)
		click, err := owner.ClickBrowserPageWithResponse(ctx, project, sessionID, apigen.ClickBrowserPageJSONRequestBody{Selector: "#submit"})
		if err != nil || click.StatusCode() != http.StatusNoContent {
			t.Fatalf("click submit: %v (%v)", click, err)
		}
		select {
		case result := <-received:
			if result.filename != source.filename || result.digest != source.digest {
				t.Fatalf("wrong uploaded %s: %+v", source.kind, result)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("local server did not receive uploaded file")
		}
		navigation, err := owner.NavigateBrowserSessionWithResponse(ctx, project, sessionID,
			apigen.NavigateBrowserSessionJSONRequestBody{Url: "http://" + listener.Addr().String() + "/"})
		if err != nil || navigation.StatusCode() != http.StatusOK {
			t.Fatalf("reload upload form: %v (%v)", navigation, err)
		}
	}
	t.Log("screenshot and download from a closed session reached the real HTML upload form with their original SHA-256")
}

func verifyBrowserUploadProjectBoundary(t *testing.T, ctx context.Context, other *apigen.ClientWithResponses, project string, sourceID, screenshotID uuid.UUID) {
	t.Helper()
	session, err := other.CreateBrowserSessionWithResponse(ctx, project)
	if err != nil || session.StatusCode() != http.StatusCreated || session.JSON201 == nil {
		t.Fatalf("create other project's session: %v (%v)", session, err)
	}
	defer func() {
		closed, err := other.CloseBrowserSessionWithResponse(ctx, project, session.JSON201.Id)
		if err != nil || closed.StatusCode() != http.StatusOK {
			t.Errorf("close other project's session: %v (%v)", closed, err)
		}
	}()
	result, err := other.AttachBrowserArtifactWithResponse(ctx, project, session.JSON201.Id, apigen.AttachBrowserArtifactJSONRequestBody{
		ArtifactKind: "screenshot", SourceSessionId: sourceID, ArtifactId: screenshotID, Selector: "#file",
	})
	if err != nil || result.StatusCode() != http.StatusNotFound {
		t.Fatalf("cross-project artifact in an authorized target session: %v (%v), want HTTP 404", result, err)
	}
	t.Log("own target session cannot read a screenshot belonging to another project")
}
