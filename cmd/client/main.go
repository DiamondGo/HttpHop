package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/DiamondGo/HttpHop/internal/client"
	"github.com/DiamondGo/HttpHop/internal/config"
)

func main() {
	configPath := flag.String("config", "configs/local/client.yaml", "path to client config")
	allowDuplicate := flag.Bool("allow-duplicate", false, "allow starting when another httphop-client is already running")
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

	releaseLock, err := client.AcquireProcessLock(*allowDuplicate)
	if err != nil {
		panic(err)
	}
	defer releaseLock()

	logger.Info("httphop-client starting",
		zap.Int("pid", os.Getpid()),
		zap.String("config", *configPath),
		zap.Int("services", len(configs)),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		reason := "signal"
		if err := context.Cause(ctx); err != nil {
			reason = err.Error()
		}
		logger.Info("client shutdown requested",
			zap.Int("pid", os.Getpid()),
			zap.String("reason", reason),
		)
	}()

	var wg sync.WaitGroup
	for _, cfg := range configs {
		svcLogger := logger.With(zap.String("service", cfg.ClientID))
		svcLogger.Info("starting service", zap.String("target", cfg.Local.Target))
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				cli := client.New(cfg, svcLogger)
				err := cli.Run(ctx)
				if err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}
					svcLogger.Error("service exited with error", zap.Error(err))
					return
				}
				// Run returned nil → superseded. Restart the service
				// so a transient supersession doesn't kill the tunnel
				// permanently.
				svcLogger.Warn("service superseded, restarting")
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
			}
		}()
	}

	wg.Wait()
	logger.Info("client stopped", zap.Int("pid", os.Getpid()))
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
