package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"

	"github.com/DiamondGo/HttpHop/internal/client"
	"github.com/DiamondGo/HttpHop/internal/config"
)

func main() {
	configPath := flag.String("config", "configs/local/client.yaml", "path to client config")
	flag.Parse()

	configs, err := config.LoadClient(*configPath)
	if err != nil {
		panic(err)
	}

	logger, err := newLogger(configs[0].Logging.Level)
	if err != nil {
		panic(err)
	}
	defer logger.Sync()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	g, ctx := errgroup.WithContext(ctx)
	for _, cfg := range configs {
		svcLogger := logger.With(zap.String("service", cfg.ClientID))
		cli := client.New(cfg, svcLogger)
		svcLogger.Info("starting service", zap.String("target", cfg.Local.Target))
		g.Go(func() error {
			return cli.Run(ctx)
		})
	}

	if err := g.Wait(); err != nil && err != context.Canceled {
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
