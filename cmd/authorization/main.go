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
	"card-issuer-api/internal/vault/development"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
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
	if cfg.VaultMode != "development" {
		return errors.New("an external PCI-compliant credential vault is required")
	}
	credentialVault, err := development.NewClient(cfg.DevelopmentVaultURL)
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
	banks, err := certificateBanks()
	if err != nil {
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

func certificateBanks() (map[string]string, error) {
	fromEnvironment := strings.TrimSpace(os.Getenv("AUTHORIZATION_CLIENT_CERTIFICATES"))
	mappingFile := strings.TrimSpace(os.Getenv("AUTHORIZATION_CLIENT_CERTIFICATES_FILE"))
	if (fromEnvironment == "" && mappingFile == "") || (fromEnvironment != "" && mappingFile != "") {
		return nil, errors.New("exactly one client certificate mapping source is required")
	}
	data := []byte(fromEnvironment)
	if mappingFile != "" {
		var err error
		data, err = os.ReadFile(mappingFile)
		if err != nil {
			return nil, errors.New("client certificate mapping file is unavailable")
		}
	}
	banks := map[string]string{}
	if err := json.Unmarshal(data, &banks); err != nil || len(banks) == 0 {
		return nil, errors.New("client certificate mappings are required")
	}
	return banks, nil
}

func healthcheck() int {
	address := os.Getenv("AUTHORIZATION_HTTP_ADDR")
	if address == "" {
		return 1
	}
	certificate, err := tls.LoadX509KeyPair(os.Getenv("AUTHORIZATION_HEALTH_CERT_FILE"), os.Getenv("AUTHORIZATION_HEALTH_KEY_FILE"))
	if err != nil {
		return 1
	}
	caPEM, err := os.ReadFile(os.Getenv("AUTHORIZATION_SERVER_CA_FILE"))
	if err != nil {
		return 1
	}
	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(caPEM) {
		return 1
	}
	connection, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", port(address)), &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: rootCAs, Certificates: []tls.Certificate{certificate}, ServerName: "localhost"})
	if err != nil {
		return 1
	}
	defer connection.Close()
	return 0
}

func port(address string) string {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	return port
}
