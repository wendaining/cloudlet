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
	"time"

	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/gateway"
	"github.com/wendaining/cloudlet/internal/routes"
)

func Run(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	var store *database.Store
	if cfg.DatabaseURL != "" {
		if len(cfg.SecretKey) != 32 {
			return errors.New("management environment requires a 32-byte secret key")
		}
		var err error
		store, err = database.Open(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer store.SQL.Close()
		if err := store.CheckVersion(ctx); err != nil {
			return err
		}
		if err := store.CheckSecretKey(ctx, cfg.SecretKey); err != nil {
			return fmt.Errorf("validate platform secret key before startup: %w", err)
		}
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
	management, closeSessions, err := controlplane.NewHandlerWithMCP(store, cfg, logger, endpoint)
	if err != nil {
		return fmt.Errorf("configure management API: %w", err)
	}
	defer closeSessions()
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
