package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/DiamondGo/HttpHop/internal/client"
	"github.com/DiamondGo/HttpHop/internal/config"
)

func main() {
	configPath := flag.String("config", "configs/client.example.yaml", "path to client config")
	flag.Parse()

	cfg, err := config.LoadClient(*configPath)
	if err != nil {
		panic(err)
	}

	logger, err := newLogger(cfg.Logging.Level)
	if err != nil {
		panic(err)
	}
	defer logger.Sync()

	cli := client.New(cfg, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.Run(ctx); err != nil && err != context.Canceled {
		logger.Fatal("client exited", zap.Error(err))
	}
	logger.Info("client stopped")
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
