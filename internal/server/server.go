package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/browser"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/gateway"
	"github.com/wendaining/cloudlet/internal/routes"
	"github.com/wendaining/cloudlet/internal/staticdeploy"
	"github.com/wendaining/cloudlet/internal/workerpublish"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) (runErr error) {
	var store *database.Store
	var sites *staticdeploy.Service
	if cfg.DatabaseURL != "" {
		if len(cfg.SecretKey) != 32 {
			return errors.New("management environment requires a 32-byte secret key")
		}
		var err error
		store, err = database.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer func() { runErr = errors.Join(runErr, store.SQL.Close()) }()
		if err := store.CheckVersion(ctx); err != nil {
			return err
		}
		if err := store.CheckSecretKey(ctx, cfg.SecretKey); err != nil {
			return fmt.Errorf("validate platform secret key before startup: %w", err)
		}
		sites, err = staticdeploy.New(store, cfg.SiteStorageDirectory)
		if err != nil {
			return err
		}
		defer func() {
			if err := sites.Close(); err != nil {
				logger.Error("close static site storage", "error", err)
			}
		}()
	}
	var browserService *browser.Service
	if cfg.BrowserImage != "" {
		docker, err := client.New(client.FromEnv)
		if err != nil {
			return fmt.Errorf("configure Browser Run Docker client: %w", err)
		}
		defer docker.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		browserService, err = browser.NewService(browserCtx, store, docker, cfg.Instance, cfg.BrowserImage, cfg.BrowserSeccomp,
			filepath.Join(cfg.SiteStorageDirectory, "browser-screenshots"), cfg.SecretKey)
		cancel()
		if err != nil {
			return fmt.Errorf("configure Browser Run: %w", err)
		}
		defer func() {
			if err := browserService.Close(); err != nil {
				logger.Error("close browser screenshot storage", "error", err)
			}
		}()
	}
	proxy, err := gateway.NewHandler(cfg, logger)
	if err != nil {
		return err
	}
	if store != nil {
		publisher, err := routes.NewPublisher(store, proxy)
		if err != nil {
			return err
		}
		if _, err := publisher.Sync(ctx); err != nil {
			return fmt.Errorf("restore Gateway routes: %w", err)
		}
	}
	var publication *workerpublish.Service
	if cfg.Workers != nil {
		docker, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			return fmt.Errorf("configure Worker Docker client: %w", err)
		}
		defer func() { runErr = errors.Join(runErr, docker.Close()) }()
		startupCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		publication, err = workerpublish.NewService(startupCtx, store, docker, proxy, *cfg.Workers, cfg.Instance)
		cancel()
		if err != nil {
			return fmt.Errorf("configure Worker publication: %w", err)
		}
		defer func() { runErr = errors.Join(runErr, publication.Close()) }()
	}
	apiListener, err := net.Listen("tcp", cfg.APIAddress)
	if err != nil {
		return fmt.Errorf("listen management API: %w", err)
	}
	defer apiListener.Close()
	gatewayListener, err := net.Listen("tcp", cfg.GatewayAddress)
	if err != nil {
		return fmt.Errorf("listen gateway: %w", err)
	}
	defer gatewayListener.Close()
	endpoint, err := managementAPIEndpoint(apiListener.Addr())
	if err != nil {
		return fmt.Errorf("resolve management API endpoint: %w", err)
	}
	management, closeSessions, err := controlplane.NewHandlerWithMCPAndWorkers(store, sites, browserService, cfg, logger, endpoint, publication)
	if err != nil {
		return fmt.Errorf("configure management API: %w", err)
	}
	defer closeSessions()
	if browserService != nil {
		reaperCtx, cancelReaper := context.WithCancel(ctx)
		reaperDone := make(chan struct{})
		go func() {
			defer close(reaperDone)
			browserService.RunReaper(reaperCtx, logger)
		}()
		defer func() {
			cancelReaper()
			<-reaperDone
		}()
	}
	api := newServer(management)
	front := newServer(proxy)
	results := make(chan error, 2)
	go func() { results <- api.Serve(apiListener) }()
	go func() { results <- front.Serve(gatewayListener) }()
	logger.Info("cloudlet started", "pid", os.Getpid(), "api_address", apiListener.Addr().String(), "gateway_address", gatewayListener.Addr().String(), "routes", len(cfg.Routes))
	var serveError error
	consumed := 0
	select {
	case <-ctx.Done():
	case serveError = <-results:
		consumed = 1
	}
	if errors.Is(serveError, http.ErrServerClosed) {
		serveError = nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownError := errors.Join(api.Shutdown(shutdownCtx), front.Shutdown(shutdownCtx))
	if shutdownError != nil {
		shutdownError = errors.Join(shutdownError, api.Close(), front.Close())
	}
	for consumed < 2 {
		if err := <-results; !errors.Is(err, http.ErrServerClosed) {
			serveError = errors.Join(serveError, err)
		}
		consumed++
	}
	logger.Info("cloudlet stopped")
	return errors.Join(serveError, shutdownError)
}

func newServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func managementAPIEndpoint(address net.Addr) (string, error) {
	host, port, err := net.SplitHostPort(address.String())
	if err != nil {
		return "", fmt.Errorf("parse listener address %q: %w", address, err)
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	} else if host == "::" {
		host = "::1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String(), nil
}
