//go:build integration

package database_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
)

func verifyBrowserPageWait(t *testing.T, ctx context.Context, owner, other *apigen.ClientWithResponses, project string, sessionID uuid.UUID, fixture string) {
	t.Helper()
	gateway, err := url.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(gateway.Hostname(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	imageReady := make(chan struct{})
	server := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/image.svg" {
				select {
				case <-imageReady:
					w.Header().Set("Content-Type", "image/svg+xml")
					fmt.Fprint(w, `<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"/>`)
				case <-r.Context().Done():
				}
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!doctype html><title>Cloudlet Wait Test</title>
				<img src="/image.svg"><p style="display:none">Invisible marker</p>
				<script>window.addEventListener('load', () => {
				  setTimeout(() => {
				    const marker = document.createElement('p');
				    marker.textContent = 'Delayed marker';
				    document.body.append(marker);
				  }, 250);
				});</script>`)
		}),
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	wait := func(api *apigen.ClientWithResponses, condition string, text *string, timeout, want int) {
		t.Helper()
		result, err := api.WaitForBrowserPageWithResponse(ctx, project, sessionID, apigen.WaitForBrowserPageJSONRequestBody{
			Condition: apigen.WaitForBrowserPageRequestCondition(condition), Text: text, TimeoutMs: &timeout,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.StatusCode() != want {
			t.Fatalf("wait %q: HTTP %d (%s), want %d", condition, result.StatusCode(), result.Body, want)
		}
	}
	wait(owner, "ready", nil, 250, http.StatusConflict)
	wait(other, "ready", nil, 250, http.StatusNotFound)
	navigation, err := owner.NavigateBrowserSessionWithResponse(ctx, project, sessionID, apigen.NavigateBrowserSessionJSONRequestBody{
		Url: "http://" + listener.Addr().String() + "/",
	})
	if err != nil || navigation.StatusCode() != http.StatusOK {
		t.Fatalf("navigate to wait fixture: %v (%v)", navigation, err)
	}
	wait(owner, "text", nil, 200, http.StatusBadRequest)
	wait(owner, "ready", nil, 200, http.StatusGatewayTimeout)
	close(imageReady)
	wait(owner, "ready", nil, 3000, http.StatusNoContent)
	wait(owner, "text", new("Delayed marker"), 3000, http.StatusNoContent)
	wait(owner, "text", new("Invisible marker"), 200, http.StatusGatewayTimeout)
	wait(owner, "text", new("Missing marker"), 200, http.StatusGatewayTimeout)
	wait(owner, "text", new("Delayed marker"), 3000, http.StatusNoContent)
	t.Log("real Chromium waited for a blocked image and delayed text; hidden and absent text returned 504; session remained usable and cross-project wait was denied")
}
