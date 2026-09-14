package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"card-issuer-api/internal/auth"
	"card-issuer-api/internal/config"
	"card-issuer-api/internal/database"
	"card-issuer-api/internal/server"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if code := run(logger); code != 0 {
		os.Exit(code)
	}
}

func run(logger *slog.Logger) int {
	cfg, err := config.FromEnvironment()
	if err != nil {
		logger.Error("invalid application configuration", "reason", err.Error())
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	signer, err := auth.NewSigner(cfg.JWTPrivateKey, cfg.JWTIssuer, cfg.JWTAudience)
	if err != nil {
		logger.Error("invalid authentication configuration")
		return 1
	}
	authPool, err := database.Open(ctx, cfg.AuthURL)
	if err != nil {
		logger.Error("authentication database pool initialization failed")
		return 1
	}
	defer authPool.Close()
	control, err := database.Open(ctx, cfg.ControlURL)
	if err != nil {
		logger.Error("control database pool initialization failed")
		return 1
	}
	defer control.Close()
	shard, err := database.Open(ctx, cfg.ShardURL)
	if err != nil {
		logger.Error("shard database pool initialization failed")
		return 1
	}
	defer shard.Close()
	listener, err := net.Listen("tcp", cfg.HTTPAddress)
	if err != nil {
		logger.Error("HTTP listener could not start")
		return 1
	}
	logger.Info("HTTP server started", "address", listener.Addr().String())
	if err := server.Serve(ctx, listener, server.Handler(authPool.Ping, control.Ping, shard.Ping, auth.NewService(authPool, signer)), logger); err != nil {
		logger.Error("HTTP server stopped unexpectedly")
		return 1
	}
	logger.Info("HTTP server stopped")
	return 0
}

func healthcheck() int {
	address := os.Getenv("HTTP_ADDR")
	if address == "" {
		address = ":8080"
	}
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return 1
	}
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/health/ready")
	if err != nil {
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
