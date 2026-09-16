// devvault runs the non-durable credential vault used only by local Compose.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"card-issuer-api/internal/config"
	"card-issuer-api/internal/vault"
	"card-issuer-api/internal/vault/development"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("development vault stopped", "reason", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.DevelopmentVaultFromEnvironment()
	if err != nil {
		return errors.New("invalid development vault configuration")
	}
	credentialVault, err := vault.NewInMemory(cfg.Environment, cfg.TestVaultKey)
	if err != nil {
		return errors.New("development vault initialization failed")
	}
	listener, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return errors.New("development vault listener could not start")
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	mux := http.NewServeMux()
	mux.Handle("/v1/", development.NewHandler(credentialVault))
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	logger.Info("development vault started", "address", listener.Addr().String())
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func healthcheck() int {
	address := os.Getenv("DEV_VAULT_ADDR")
	if address == "" {
		address = ":8081"
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return 1
	}
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/health/live")
	if err != nil {
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
