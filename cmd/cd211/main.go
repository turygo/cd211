package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/turygo/cd211/internal/config"
	"github.com/turygo/cd211/internal/reconcile"
	"github.com/turygo/cd211/internal/server"
	"github.com/turygo/cd211/internal/session"
	"github.com/turygo/cd211/internal/settings"
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
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// The flag package already printed the usage text; return before
			// any runtime logging is armed.
			return nil
		}
		logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
		logger.Error("invalid command line", "error", err)
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	defer func() {
		if result == nil {
			logger.Info("runtime shut down")
		}
	}()

	rootContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	clock := reconcile.RealClock{}
	sessions, err := session.New(clock, rand.Reader, sessionTTL, sessionCapacity)
	if err != nil {
		logger.Error("runtime startup failed", "error", err)
		return err
	}

	root := &switchHandler{}
	runtimeManager := newManager(root, st, sessions, clock, logger)

	settingsConfig, completed, err := settings.Load(rootContext, st)
	if err != nil {
		logger.Error("runtime startup failed", "error", err)
		return err
	}
	if completed {
		generation, err := runtimeManager.build(rootContext, settingsConfig)
		if err != nil {
			logger.Error("runtime startup failed", "error", err)
			return err
		}
		runtimeManager.activate(generation)
	} else {
		setup, err := web.NewSetup(web.SetupConfig{
			Store:    st,
			Sessions: sessions,
			Clock:    clock,
			Dial:     nil,
			Complete: runtimeManager.Apply,
		})
		if err != nil {
			logger.Error("runtime startup failed", "error", err)
			return err
		}
		root.Store(setupModeMux(setup))
		logger.Info("setup mode active; waiting for the setup wizard")
	}

	httpServer := server.NewHTTPServer(cfg.HTTPAddress, root)
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- httpServer.ListenAndServe()
	}()
	logger.Info("runtime started", "address", cfg.HTTPAddress)

	serveStopped := false
serveLoop:
	for {
		generation := runtimeManager.currentGeneration()
		var generationDone <-chan struct{}
		if generation != nil {
			generationDone = generation.done
		}
		select {
		case err := <-serveResult:
			serveStopped = true
			if !errors.Is(err, http.ErrServerClosed) {
				logger.Error("runtime server failed", "error", err)
				result = err
			}
			break serveLoop
		case <-generationDone:
			if runtimeManager.currentGeneration() != generation {
				continue
			}
			err := generation.result
			if err == nil {
				err = errors.New("reconciler stopped unexpectedly")
			}
			logger.Error("runtime reconciler failed", "error", err)
			result = err
			break serveLoop
		case <-rootContext.Done():
			break serveLoop
		}
	}

	stop()
	runtimeManager.shutdown()
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
