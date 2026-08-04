package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/DiamondGo/HttpHop/internal/config"
	"github.com/DiamondGo/HttpHop/internal/server"
)

func main() {
	configPath := flag.String("config", "configs/local/server.yaml", "path to server config")
	flag.Parse()

	cfg, err := config.LoadServer(*configPath)
	if err != nil {
		panic(err)
	}

	logger, err := newLogger(cfg.Logging.Level)
	if err != nil {
		panic(err)
	}
	defer logger.Sync()

	srv, err := server.NewServer(cfg, logger)
	if err != nil {
		logger.Fatal("create server", zap.Error(err))
	}

	if err := srv.Start(); err != nil {
		logger.Fatal("start server", zap.Error(err))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Tunnel.SessionTimeout)
	defer cancel()
	if err := srv.Stop(shutdownCtx); err != nil {
		logger.Error("shutdown error", zap.Error(err))
		os.Exit(1)
	}
}

func newLogger(level string) (*zap.Logger, error) {
	cfg := zap.NewProductionConfig()
	if level != "" {
		if err := cfg.Level.UnmarshalText([]byte(level)); err != nil {
			return nil, err
		}
	}
	return cfg.Build()
}
