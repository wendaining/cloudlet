package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
)

func TestBrowserClientCreatesNavigatesAndClosesSession(t *testing.T) {
	projectID := uuid.New()
	sessionID := uuid.New()
	pageID := uuid.New()
	expiresAt := time.Date(2026, 11, 28, 2, 0, 0, 0, time.UTC)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "cloudlet_dev_session=browser-secret" {
			t.Errorf("Cookie = %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Content-Type", "application/json")
		basePath := "/api/v1/projects/" + projectID.String() + "/browser-sessions"
		switch {
		case r.Method == http.MethodPost && r.URL.Path == basePath:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(apigen.BrowserSession{
				Id:            sessionID,
				Endpoint:      basePath + "/" + sessionID.String(),
				ExpiresAt:     expiresAt,
				Status:        apigen.BrowserSessionStatusReady,
				DesiredStatus: apigen.BrowserSessionStatusReady,
				ActualStatus:  apigen.BrowserSessionStatusReady,
			})
		case r.Method == http.MethodPost && r.URL.Path == basePath+"/"+sessionID.String()+"/navigate":
			var body apigen.NavigateBrowserSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode navigate body: %v", err)
			}
			if body.Url != "https://example.test/home" {
				t.Fatalf("navigate url = %q", body.Url)
			}
			if body.PageId == nil || *body.PageId != pageID {
				t.Fatalf("navigate page_id = %v, want %s", body.PageId, pageID)
			}
			_ = json.NewEncoder(w).Encode(apigen.BrowserNavigation{
				Url:   body.Url,
				Title: "Example Home",
			})
		case r.Method == http.MethodDelete && r.URL.Path == basePath+"/"+sessionID.String():
			_ = json.NewEncoder(w).Encode(apigen.BrowserSession{
				Id:            sessionID,
				Endpoint:      basePath + "/" + sessionID.String(),
				ExpiresAt:     expiresAt,
				Status:        apigen.BrowserSessionStatusClosed,
				DesiredStatus: apigen.BrowserSessionStatusClosed,
				ActualStatus:  apigen.BrowserSessionStatusClosed,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := api.SetSessionCookie("cloudlet_dev_session=browser-secret"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	session, err := api.CreateBrowserSession(ctx, projectID.String())
	if err != nil {
		t.Fatalf("CreateBrowserSession() error = %v", err)
	}
	if session.Id != sessionID || session.Status != apigen.BrowserSessionStatusReady {
		t.Fatalf("CreateBrowserSession() = %+v", session)
	}

	nav, err := api.NavigateBrowserSession(ctx, projectID.String(), sessionID, "https://example.test/home", &pageID)
	if err != nil {
		t.Fatalf("NavigateBrowserSession() error = %v", err)
	}
	if nav.Url != "https://example.test/home" || nav.Title != "Example Home" {
		t.Fatalf("NavigateBrowserSession() = %+v", nav)
	}

	closed, err := api.CloseBrowserSession(ctx, projectID.String(), sessionID)
	if err != nil {
		t.Fatalf("CloseBrowserSession() error = %v", err)
	}
	if closed.Status != apigen.BrowserSessionStatusClosed {
		t.Fatalf("CloseBrowserSession() = %+v", closed)
	}
}

func TestBrowserClientValidatesInputsAndSurfacesErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(apigen.Error{Message: "只有项目所有者和开发者可以操作浏览器会话"})
	}))
	defer server.Close()

	api, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := api.CreateBrowserSession(ctx, "not-a-uuid"); err == nil {
		t.Fatal("CreateBrowserSession accepted invalid project ID")
	}
	if _, err := api.CreateBrowserSession(ctx, uuid.Nil.String()); err == nil {
		t.Fatal("CreateBrowserSession accepted nil project ID")
	}
	if _, err := api.NavigateBrowserSession(ctx, uuid.NewString(), uuid.Nil, "http://example.test", nil); err == nil {
		t.Fatal("NavigateBrowserSession accepted nil session ID")
	}
	if _, err := api.NavigateBrowserSession(ctx, uuid.NewString(), uuid.New(), "   ", nil); err == nil {
		t.Fatal("NavigateBrowserSession accepted empty URL")
	}
	nilPage := uuid.Nil
	if _, err := api.NavigateBrowserSession(ctx, uuid.NewString(), uuid.New(), "http://example.test", &nilPage); err == nil {
		t.Fatal("NavigateBrowserSession accepted nil page ID")
	}

	_, err = api.CreateBrowserSession(ctx, uuid.NewString())
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "只有项目所有者和开发者可以操作浏览器会话") {
		t.Fatalf("CreateBrowserSession error = %v, want HTTP 403 message", err)
	}
}
