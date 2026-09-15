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

	"card-issuer-api/internal/database"
	domain "card-issuer-api/internal/domain"
	"card-issuer-api/internal/executor"
	controlrepository "card-issuer-api/internal/repository/control"
	routingrepository "card-issuer-api/internal/repository/routing"
	shardrepository "card-issuer-api/internal/repository/shard"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("executor stopped", "error", err)
		os.Exit(1)
	}
}

func healthcheck() int {
	address := os.Getenv("EXECUTOR_HTTP_ADDR")
	if address == "" {
		return 1
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

func run(logger *slog.Logger) error {
	config, err := executor.FromEnvironment()
	if err != nil {
		return errors.New("invalid executor configuration")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	control, err := database.Open(ctx, config.ControlURL)
	if err != nil {
		return errors.New("control database pool initialization failed")
	}
	defer control.Close()
	shard, err := database.Open(ctx, config.ShardURL)
	if err != nil {
		return errors.New("shard database pool initialization failed")
	}
	defer shard.Close()
	service := executor.New(config, shardrepository.NewExecutor(shard), directory{users: controlrepository.New(control), routes: routingrepository.New(control)})
	listener, err := net.Listen("tcp", config.OperationalAddress)
	if err != nil {
		return errors.New("executor listener could not start")
	}
	server := &http.Server{Handler: executor.Handler(service, control.Ping, shard.Ping), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- service.Run(ctx) }()
	go func() {
		<-ctx.Done()
		shutdownCtx, stop := context.WithTimeout(context.Background(), config.DrainTimeout)
		defer stop()
		_ = server.Shutdown(shutdownCtx)
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("executor started", "address", listener.Addr().String())
	select {
	case err = <-errCh:
		shutdownCtx, stop := context.WithTimeout(context.Background(), config.DrainTimeout)
		defer stop()
		_ = server.Shutdown(shutdownCtx)
		serverErr := <-serveErr
		if err != nil {
			return err
		}
		if serverErr != nil && !errors.Is(serverErr, http.ErrServerClosed) {
			return errors.New("executor HTTP server stopped unexpectedly")
		}
		return nil
	case err = <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return errors.New("executor HTTP server stopped unexpectedly")
		}
		if err = <-errCh; err != nil {
			return err
		}
		return nil
	}
}

type directory struct {
	users  *controlrepository.Store
	routes *routingrepository.Store
}

func (d directory) User(ctx context.Context, id string) (domain.DirectoryUser, error) {
	return d.users.User(ctx, id)
}

func (d directory) Banks(ctx context.Context) ([]domain.Route, error) {
	return d.routes.Banks(ctx)
}
