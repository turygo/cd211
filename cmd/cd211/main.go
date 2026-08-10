package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/turygo/cd211/internal/config"
	"github.com/turygo/cd211/internal/outbox"
	"github.com/turygo/cd211/internal/reconcile"
	"github.com/turygo/cd211/internal/server"
	"github.com/turygo/cd211/internal/session"
	"github.com/turygo/cd211/internal/settings"
	"github.com/turygo/cd211/internal/store"
	"github.com/turygo/cd211/internal/torrentmeta"
	"github.com/turygo/cd211/internal/web"
	"github.com/turygo/cd211/internal/webhook"
)

const (
	cloudRPCTimeout        = 30 * time.Second
	reconcileLeaseDuration = 3 * time.Minute
	sessionTTL             = 24 * time.Hour
	sessionCapacity        = 256
	webhookPruneInterval   = 24 * time.Hour
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

	// The webhook dispatcher is process-owned: it starts as soon as the store
	// is open and runs until process shutdown, surviving settings hot swaps
	// that rebuild runtime generations. Every shutdown path cancels and awaits
	// it before the store closes.
	dispatcher, err := startWebhookDispatcher(rootContext, st, logger)
	if err != nil {
		logger.Error("runtime startup failed", "error", err)
		return err
	}
	defer func() {
		if err := dispatcher.shutdown(); err != nil {
			logger.Error("webhook dispatcher shutdown failed", "error", err)
			if result == nil {
				result = err
			}
		}
	}()

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
		case <-dispatcher.done:
			// Observing completion is non-destructive: the deferred shutdown
			// below awaits the same done signal, so store closure is reachable
			// on every path. When the root context is cancelled the completion
			// is the normal shutdown path; otherwise the dispatcher stopped
			// unexpectedly and that is fatal.
			if err := dispatcher.exitError(rootContext); err != nil {
				logger.Error("runtime webhook dispatcher failed", "error", err)
				result = err
			}
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

// webhookProcess owns the process-lifetime webhook dispatcher: its child
// context, exit signal, and teardown. It is deliberately independent of
// runtime generations, so settings hot swaps never duplicate or interrupt it.
//
// Completion is idempotent, mirroring runtime: the dispatcher goroutine
// publishes result before closing done, so any number of waiters (the serve
// loop and the deferred shutdown) can observe the same completion without
// racing over a single channel value. result is read only after receiving
// from done, which makes the write visible.
type webhookProcess struct {
	cancel context.CancelFunc
	done   chan struct{}
	result error
}

// startWebhookDispatcher constructs the stdlib webhook HTTP client and the
// process-owned dispatcher with the plan's fixed constants, then runs it on a
// child context of ctx. It starts as soon as the store is open, before
// settings load, so it may run during setup mode. Run returns only on
// cancellation or an unrecoverable invariant; an exit while the process is
// active is treated as fatal by the main serve loop. Shutdown must cancel and
// await it before the store closes.
func startWebhookDispatcher(ctx context.Context, repo webhook.Repository, logger *slog.Logger) (*webhookProcess, error) {
	dispatcher, err := webhook.New(webhook.Config{
		Owner:          workerOwner() + ":webhook",
		LeaseDuration:  outbox.LeaseDuration,
		RequestTimeout: outbox.HTTPTimeout,
		WorkerCount:    outbox.DefaultWorkers,
		MaxIdleWait:    outbox.MaxIdlePoll,
		PruneInterval:  webhookPruneInterval,
		Version:        "unknown",
	}, repo, webhook.NewHTTPClient(outbox.HTTPTimeout), webhook.RealClock{}, logger)
	if err != nil {
		return nil, fmt.Errorf("webhook dispatcher: %w", err)
	}
	dispatcherContext, cancel := context.WithCancel(ctx)
	w := &webhookProcess{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go func() {
		w.result = dispatcher.Run(dispatcherContext)
		close(w.done)
	}()
	return w, nil
}

// shutdown cancels the dispatcher and waits for every worker to exit. It must
// complete before the store closes; after it returns the dispatcher no longer
// touches the store. It is idempotent: repeated or concurrent calls observe
// the same completion and return the same stored result.
func (w *webhookProcess) shutdown() error {
	w.cancel()
	<-w.done
	return w.result
}

// exitError classifies a completed dispatcher for the serve loop. It must be
// called only after done is closed. When the root context is already
// cancelled, the completion is part of normal shutdown and nil is returned,
// resolving the root-cancel vs completion race in favour of normal shutdown.
// Otherwise the stored result is returned as a fatal error, wrapping a nil
// result so an unexpected early exit reads as a dispatcher failure.
func (w *webhookProcess) exitError(rootCtx context.Context) error {
	if rootCtx.Err() != nil {
		return nil
	}
	err := w.result
	if err == nil {
		err = errors.New("webhook dispatcher stopped unexpectedly")
	}
	return err
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
