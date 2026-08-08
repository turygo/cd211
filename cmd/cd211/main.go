package main

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/turygo/cd211/internal/clouddrive"
	"github.com/turygo/cd211/internal/config"
	"github.com/turygo/cd211/internal/creds"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/httpapi"
	"github.com/turygo/cd211/internal/reconcile"
	"github.com/turygo/cd211/internal/server"
	"github.com/turygo/cd211/internal/session"
	"github.com/turygo/cd211/internal/store"
	"github.com/turygo/cd211/internal/torrentmeta"
	"github.com/turygo/cd211/internal/web"
)

const (
	cloudRPCTimeout        = 30 * time.Second
	reconcileLeaseDuration = 3 * time.Minute
	sessionTTL             = 24 * time.Hour
	sessionCapacity        = 256
)

func main() {
	if err := run(); err != nil {
		os.Exit(1)
	}
}

func run() (result error) {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	defer func() {
		if result == nil {
			logger.Info("runtime shut down")
		}
	}()

	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		logger.Error("runtime startup failed", "error", err)
		return err
	}

	st, err := store.Open(rootContext, cfg.DatabasePath)
	if err != nil {
		logger.Error("runtime startup failed", "error", err)
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			logger.Error("store close failed", "error", err)
			if result == nil {
				result = err
			}
		}
	}()

	cloud, err := clouddrive.Dial(cfg.CD2Address, cfg.CD2Username, cfg.CD2Password, cloudRPCTimeout, cfg.CD2Insecure)
	if err != nil {
		logger.Error("runtime startup failed", "error", err)
		return err
	}
	defer func() {
		if err := cloud.Close(); err != nil {
			logger.Error("clouddrive close failed", "error", err)
			if result == nil {
				result = err
			}
		}
	}()

	files, err := fsafe.New(cfg.LocalRoot)
	if err != nil {
		logger.Error("runtime startup failed", "error", err)
		return err
	}
	cfg.LocalRoot = files.LocalRoot()
	clock := reconcile.RealClock{}
	sessions, err := session.New(clock, rand.Reader, sessionTTL, sessionCapacity)
	if err != nil {
		logger.Error("runtime startup failed", "error", err)
		return err
	}
	coordinator, err := reconcile.New(reconcile.Config{
		Owner:          workerOwner(),
		LeaseDuration:  reconcileLeaseDuration,
		PollInterval:   15 * time.Second,
		OfflineTimeout: cfg.OfflineTimeout,
		CopyTimeout:    cfg.CopyTimeout,
		VerifyTimeout:  cfg.VerifyTimeout,
		WorkerCount:    4,
	}, st, cloud, files, clock, logger)
	if err != nil {
		logger.Error("runtime startup failed", "error", err)
		return err
	}

	credentials, err := creds.New(st)
	if err != nil {
		logger.Error("runtime startup failed", "error", err)
		return err
	}
	limits := metadataLimits()
	api, err := httpapi.New(httpapi.Config{
		CloudRoot: cfg.CloudRoot, LocalRoot: cfg.LocalRoot,
		TorrentLimits: limits, MaxRequestBytes: int64(limits.MaxInputBytes) + 64<<10,
	}, credentials, st, sessions, clock, coordinator, files)
	if err != nil {
		logger.Error("runtime startup failed", "error", err)
		return err
	}
	ui, err := web.New(web.Config{
		CloudRoot: cfg.CloudRoot, LocalRoot: cfg.LocalRoot,
	}, credentials, st, sessions, clock, coordinator, cloud, files)
	if err != nil {
		logger.Error("runtime startup failed", "error", err)
		return err
	}

	health := server.NewHandler(
		func(requestContext context.Context) error {
			return st.PingContext(requestContext)
		},
		func(requestContext context.Context) error {
			return st.Ready(requestContext, cfg.LocalRoot)
		},
		cloud.Check,
	)
	root := http.NewServeMux()
	root.Handle("/healthz", health)
	root.Handle("/readyz", health)
	root.Handle("/api/v2/", api)
	root.Handle("/", ui)
	httpServer := server.NewHTTPServer(cfg.HTTPAddress, root)

	serveResult := make(chan error, 1)
	go func() {
		serveResult <- httpServer.ListenAndServe()
	}()
	reconcileResult := make(chan error, 1)
	go func() {
		reconcileResult <- coordinator.Run(rootContext)
	}()
	logger.Info("runtime started", "address", cfg.HTTPAddress)

	serveStopped, reconcileStopped := false, false
	select {
	case err := <-serveResult:
		serveStopped = true
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("runtime server failed", "error", err)
			result = err
		}
	case err := <-reconcileResult:
		reconcileStopped = true
		if err == nil && rootContext.Err() != nil {
			break
		}
		if err == nil {
			err = errors.New("reconciler stopped unexpectedly")
		}
		logger.Error("runtime reconciler failed", "error", err)
		result = err
	case <-rootContext.Done():
	}

	stop()
	shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := httpServer.Shutdown(shutdownContext); err != nil {
		logger.Error("runtime shutdown failed", "error", err)
		if result == nil {
			result = err
		}
	}
	cancel()
	if !serveStopped {
		if err := <-serveResult; !errors.Is(err, http.ErrServerClosed) {
			logger.Error("runtime server failed", "error", err)
			if result == nil {
				result = err
			}
		}
	}
	if !reconcileStopped {
		if err := <-reconcileResult; err != nil {
			logger.Error("runtime reconciler failed", "error", err)
			if result == nil {
				result = err
			}
		}
	}
	return result
}

func workerOwner() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "cd211"
	}
	return host + ":" + strconv.Itoa(os.Getpid())
}

func metadataLimits() torrentmeta.Limits {
	return torrentmeta.Limits{
		MaxInputBytes:     16 << 20,
		MaxInfoBytes:      12 << 20,
		MaxFiles:          20_000,
		MaxNameBytes:      1 << 10,
		MaxPathBytes:      4 << 10,
		MaxComponentBytes: 255,
		MaxTrackerCount:   256,
		MaxTrackerBytes:   8 << 10,
		MaxTotalSize:      1 << 60,
	}
}
