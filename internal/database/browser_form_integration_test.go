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

func verifyBrowserForm(t *testing.T, ctx context.Context, owner, other *apigen.ClientWithResponses, project string, id uuid.UUID, fixture string) {
	t.Helper()
	original, err := url.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(original.Hostname(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	submitted := make(chan url.Values, 1)
	server := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.Method == http.MethodPost && r.URL.Path == "/submit" {
				if err := r.ParseForm(); err != nil {
					http.Error(w, "invalid form", http.StatusBadRequest)
					return
				}
				select {
				case submitted <- r.PostForm:
				default:
				}
				fmt.Fprint(w, "<!doctype html><title>Form submitted</title>")
				return
			}
			fmt.Fprint(w, `<!doctype html><title>Form fixture</title><form method="post" action="/submit">
<input id="name" name="name" value="initial"><input id="extra" name="extra" value="clear me">
<select id="team" name="team"><option value="">Choose</option><option value="browser">Browser</option></select>
<button id="submit" type="submit">Submit</button></form>`)
		}),
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	target := "http://" + listener.Addr().String() + "/"
	page, err := owner.NavigateBrowserSessionWithResponse(ctx, project, id, apigen.NavigateBrowserSessionJSONRequestBody{Url: target})
	if err != nil || page.StatusCode() != http.StatusOK || page.JSON200 == nil || page.JSON200.Title != "Form fixture" {
		t.Fatalf("navigate form: %v (%v)", page, err)
	}
	apply := func(api *apigen.ClientWithResponses, action, selector, value string, timeout *int, expected int) {
		t.Helper()
		input := apigen.ApplyBrowserFormActionJSONRequestBody{
			Action:   apigen.BrowserFormActionRequestAction(action),
			Selector: selector, Value: value, TimeoutMs: timeout,
		}
		result, err := api.ApplyBrowserFormActionWithResponse(ctx, project, id, input)
		if err != nil || result.StatusCode() != expected {
			t.Fatalf("form action %s on %q: %v (%v), want HTTP %d", action, selector, result, err, expected)
		}
	}
	apply(other, "fill", "#name", "not allowed", nil, http.StatusNotFound)
	apply(owner, "bad-action", "#name", "", nil, http.StatusBadRequest)
	apply(owner, "fill", "input", "ambiguous", nil, http.StatusUnprocessableEntity)
	short := 200
	apply(owner, "fill", "#missing", "timeout", &short, http.StatusGatewayTimeout)
	apply(owner, "select", "#team", "nonexistent", &short, http.StatusGatewayTimeout)
	apply(owner, "fill", "#name", "浏览器组", nil, http.StatusNoContent)
	apply(owner, "fill", "#extra", "", nil, http.StatusNoContent)
	apply(owner, "select", "#team", "browser", nil, http.StatusNoContent)
	click, err := owner.ClickBrowserPageWithResponse(ctx, project, id, apigen.ClickBrowserPageJSONRequestBody{Selector: "#submit"})
	if err != nil || click.StatusCode() != http.StatusNoContent {
		t.Fatalf("submit form: %v (%v)", click, err)
	}
	select {
	case record := <-submitted:
		if record.Get("name") != "浏览器组" || record.Get("team") != "browser" || !record.Has("extra") || record.Get("extra") != "" {
			t.Fatalf("received wrong form submission: %v", record)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("form submission did not reach the local HTTP server")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	t.Log("real HTML form received the entered text, cleared input and selected option; ambiguous control HTTP 422, absent element/option HTTP 504, other project HTTP 404")
}
