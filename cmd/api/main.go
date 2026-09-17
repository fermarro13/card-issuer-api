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
	"card-issuer-api/internal/bank"
	"card-issuer-api/internal/batch"
	"card-issuer-api/internal/card"
	"card-issuer-api/internal/catalog"
	"card-issuer-api/internal/config"
	"card-issuer-api/internal/database"
	"card-issuer-api/internal/idempotency"
	authrepository "card-issuer-api/internal/repository/auth"
	controlrepository "card-issuer-api/internal/repository/control"
	routingrepository "card-issuer-api/internal/repository/routing"
	shardrepository "card-issuer-api/internal/repository/shard"
	"card-issuer-api/internal/server"
	"card-issuer-api/internal/staff"
	"card-issuer-api/internal/tenant"
	"card-issuer-api/internal/vault/development"
)

// @title Card Issuer API
// @version 1.0
// @description Public HTTP API for issuer operations. The interactive catalog is served at /swagger/index.html.
// @BasePath /
// @schemes http https
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization
// @description Enter a JWT access token as `Bearer <token>`.
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
	fingerprinter, err := idempotency.NewFingerprinter(cfg.IdempotencyKey)
	if err != nil {
		logger.Error("invalid idempotency configuration")
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
	authentication := auth.NewService(authrepository.New(authPool), signer)
	banks := bank.New(controlrepository.NewBank(authPool), routingrepository.New(control), shardrepository.NewBank(shard), cfg.ShardID, fingerprinter)
	catalogService := catalog.New(shardrepository.NewCatalog(shard), fingerprinter)
	if cfg.VaultMode != "development" {
		logger.Error("external credential vault provisioning is required")
		return 1
	}
	credentialVault, err := development.NewClient(cfg.DevelopmentVaultURL)
	if err != nil {
		logger.Error("credential vault initialization failed")
		return 1
	}
	cardService := card.New(shardrepository.NewCard(shard), credentialVault, fingerprinter)
	batchService := batch.New(shardrepository.NewBatch(shard), fingerprinter)
	staffService := staff.New(controlrepository.NewStaff(authPool), routingrepository.New(control), cfg.ShardID, fingerprinter)
	bankAccess := tenant.New(routingrepository.New(control), cfg.ShardID)
	resourceHandler := server.NewResourceHandler(authentication, server.Dependencies{
		Bank:    banks,
		Access:  bankAccess,
		Catalog: catalogService,
		Card:    cardService,
		Batch:   batchService,
		Staff:   staffService,
	}, cfg.CursorKey)
	if err := server.Serve(ctx, listener, server.HandlerWithLoginAttemptLimit(authPool.Ping, control.Ping, shard.Ping, authentication, cfg.LoginAttemptLimit, resourceHandler), logger); err != nil {
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
