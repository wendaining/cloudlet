package server_test

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/server"
)

func TestServerReleasesBothListenersOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := make(chan error, 1)
	go func() {
		result <- server.Run(ctx, config.Config{APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0"}, slog.Default())
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop after cancellation")
	}
}

func TestServerRejectsOccupiedGatewayPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	err = server.Run(context.Background(), config.Config{
		APIAddress: "127.0.0.1:0", GatewayAddress: listener.Addr().String(),
	}, slog.Default())
	if err == nil {
		t.Fatal("server started with an occupied gateway port")
	}
}
