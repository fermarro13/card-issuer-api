// authorization is the network-isolated mTLS credential verification service.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"card-issuer-api/internal/authorization"
	"card-issuer-api/internal/authorizationhttp"
	"card-issuer-api/internal/config"
	"card-issuer-api/internal/database"
	shardrepository "card-issuer-api/internal/repository/shard"
	"card-issuer-api/internal/vault"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("authorization service stopped", "reason", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.FromEnvironment()
	if err != nil {
		return errors.New("invalid application configuration")
	}
	if cfg.VaultMode != "memory" {
		return errors.New("an external PCI-compliant credential vault is required")
	}
	credentialVault, err := vault.NewInMemory(cfg.Environment, cfg.TestVaultKey)
	if err != nil {
		return errors.New("credential vault initialization failed")
	}
	tlsConfig, certificateBanks, address, err := mtlsConfiguration()
	if err != nil {
		return errors.New("invalid mTLS configuration")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	pool, err := database.Open(ctx, cfg.ShardURL)
	if err != nil {
		return errors.New("shard database pool initialization failed")
	}
	defer pool.Close()
	service := authorization.New(shardrepository.NewAuthorization(pool), authorization.NewVaultVerifier(credentialVault))
	listener, err := tls.Listen("tcp", address, tlsConfig)
	if err != nil {
		return errors.New("mTLS listener could not start")
	}
	defer listener.Close()
	httpServer := &http.Server{Handler: authorizationhttp.New(service, certificateBanks), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	logger.Info("authorization mTLS service started", "address", listener.Addr().String())
	err = httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func mtlsConfiguration() (*tls.Config, map[string]string, string, error) {
	address := os.Getenv("AUTHORIZATION_HTTP_ADDR")
	if address == "" {
		address = ":8443"
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return nil, nil, "", err
	}
	certificate, err := tls.LoadX509KeyPair(os.Getenv("AUTHORIZATION_TLS_CERT_FILE"), os.Getenv("AUTHORIZATION_TLS_KEY_FILE"))
	if err != nil {
		return nil, nil, "", err
	}
	caPEM, err := os.ReadFile(os.Getenv("AUTHORIZATION_CLIENT_CA_FILE"))
	if err != nil {
		return nil, nil, "", err
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, nil, "", errors.New("no client CA certificate")
	}
	banks := map[string]string{}
	if err := json.Unmarshal([]byte(os.Getenv("AUTHORIZATION_CLIENT_CERTIFICATES")), &banks); err != nil || len(banks) == 0 {
		return nil, nil, "", errors.New("client certificate mappings are required")
	}
	for fingerprint, bank := range banks {
		if len(fingerprint) != 64 || strings.TrimSpace(bank) == "" {
			return nil, nil, "", errors.New("invalid client certificate mapping")
		}
		if _, err := hex.DecodeString(fingerprint); err != nil {
			return nil, nil, "", errors.New("invalid client certificate mapping")
		}
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: clientCAs}, banks, address, nil
}
