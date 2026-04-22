// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/stacklok/toolhive/cmd/thv-memory/lifecycle"
	"github.com/stacklok/toolhive/pkg/memory"
	"github.com/stacklok/toolhive/pkg/memory/embedder/ollama"
	memorysqlite "github.com/stacklok/toolhive/pkg/memory/sqlite"
)

const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second
	// writeTimeout is intentionally long: SSE streams for MCP can be long-lived.
	writeTimeout    = 0
	idleTimeout     = 120 * time.Second
	shutdownTimeout = 10 * time.Second
)

func main() {
	cfgPath := os.Getenv("MEMORY_CONFIG")
	if cfgPath == "" {
		cfgPath = "/config/memory-server.yaml"
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("creating logger: %v", err)
	}
	defer logger.Sync() //nolint:errcheck

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	db, err := memorysqlite.Open(ctx, cfg.Storage.DSN)
	if err != nil {
		logger.Fatal("opening database", zap.Error(err))
	}
	defer db.Close() //nolint:errcheck

	store := memorysqlite.NewStore(db)
	vectors := memorysqlite.NewVectorStore(db)

	var embedder memory.Embedder
	switch cfg.Embedder.Provider {
	case providerOllama:
		embedder, err = ollama.New(cfg.Embedder.URL, cfg.Embedder.Model)
		if err != nil {
			logger.Fatal("creating ollama embedder", zap.Error(err))
		}
	default:
		logger.Fatal("unsupported embedder provider", zap.String("provider", cfg.Embedder.Provider))
	}

	svc, err := memory.NewService(store, vectors, embedder, logger)
	if err != nil {
		logger.Fatal("creating memory service", zap.Error(err))
	}

	job := lifecycle.New(store, logger)
	go job.Run(ctx, time.Duration(cfg.Server.LifecycleHours)*time.Hour)

	if err := serve(ctx, cfg, svc, store, logger); err != nil {
		logger.Error("server exited with error", zap.Error(err))
		os.Exit(1)
	}
}

func serve(ctx context.Context, cfg *Config, svc *memory.Service, store memory.Store, logger *zap.Logger) error {
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("creating listener: %w", err)
	}

	handler := newHandler(cfg, svc, store, logger)
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("memory MCP server listening",
			zap.String("addr", listener.Addr().String()),
			zap.String("endpoint", mcpEndpointPath),
		)
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, shutCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutCancel()
		return httpServer.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}
