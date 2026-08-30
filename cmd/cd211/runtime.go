package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/turygo/cd211/internal/clouddrive"
	"github.com/turygo/cd211/internal/creds"
	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/httpapi"
	"github.com/turygo/cd211/internal/logging"
	"github.com/turygo/cd211/internal/nativeapi"
	"github.com/turygo/cd211/internal/reconcile"
	"github.com/turygo/cd211/internal/server"
	"github.com/turygo/cd211/internal/session"
	"github.com/turygo/cd211/internal/settings"
	"github.com/turygo/cd211/internal/store"
	"github.com/turygo/cd211/internal/submission"
	"github.com/turygo/cd211/internal/web"
)

// switchHandler is the atomic HTTP root. The served handler is swapped in
// place on every runtime rebuild without a lock, so in-flight requests always
// observe exactly one complete generation.
type switchHandler struct {
	handler atomic.Pointer[http.Handler]
}

// Store atomically replaces the served handler.
func (s *switchHandler) Store(h http.Handler) {
	s.handler.Store(&h)
}

// Load returns the currently served handler, or nil before the first Store.
func (s *switchHandler) Load() http.Handler {
	pointer := s.handler.Load()
	if pointer == nil {
		return nil
	}
	return *pointer
}

func (s *switchHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := s.Load()
	if h == nil {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	h.ServeHTTP(w, r)
}

// runtime is one complete set of runtime dependencies: a cloud client, a
// reconciler, and the HTTP mux that exposes them. A generation is created by
// manager.build and retired by manager.Apply or manager.shutdown.
type runtime struct {
	cfg     settings.Config
	handler http.Handler
	cloud   *clouddrive.Client
	coord   *reconcile.Scheduler
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	result  error
}

// run drives the generation's reconciler until ctx is cancelled, then closes
// done. result is published before done closes, so receivers may read it after
// receiving from done without additional synchronization.
func (r *runtime) run() {
	r.result = r.coord.Run(r.ctx)
	close(r.done)
}

// manager builds and swaps runtime generations behind a stable HTTP root. The
// store and session store are shared by every generation for the process
// lifetime; only the cloud client and reconciler are generation-owned.
type manager struct {
	mu           sync.Mutex
	root         *switchHandler
	store        *store.Store
	sessions     *session.Store
	clock        reconcile.Clock
	logger       *slog.Logger
	logReader    *logging.Reader
	current      *runtime
	shuttingDown bool
}

func newManager(root *switchHandler, store *store.Store, sessions *session.Store, clock reconcile.Clock, logger *slog.Logger) *manager {
	return &manager{root: root, store: store, sessions: sessions, clock: clock, logger: logger}
}

// Apply builds a new runtime generation from cfg, atomically swaps it in as
// the HTTP root, starts its reconciler, then shuts down the previous
// generation. An intentionally cancelled old generation never terminates the
// process; only the current generation's reconciler failure reaches run()'s
// exit path. ctx only gates the build; once the swap starts it is ignored.
func (m *manager) Apply(ctx context.Context, cfg settings.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shuttingDown {
		return errors.New("runtime is shutting down")
	}

	next, err := m.build(ctx, cfg)
	if err != nil {
		return err
	}
	previous := m.current
	m.activateLocked(next)
	if previous != nil {
		m.stopLocked(previous)
	}
	return nil
}

// currentGeneration returns the active generation, or nil before the first
// activation.
func (m *manager) currentGeneration() *runtime {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// activate installs a freshly built generation as current and starts its
// reconciler. It is used for the initial startup generation.
func (m *manager) activate(generation *runtime) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.activateLocked(generation)
}

func (m *manager) activateLocked(next *runtime) {
	m.current = next
	m.root.Store(next.handler)
	go next.run()
}

// stopLocked cancels a retired generation, waits for its reconciler to return,
// and closes its cloud client. The error result is intentionally ignored: a
// cancelled generation is expected to stop.
func (m *manager) stopLocked(generation *runtime) {
	generation.cancel()
	<-generation.done
	if err := generation.cloud.Close(); err != nil {
		m.logger.Warn("clouddrive close failed", "error", err)
	}
}

// shutdown stops whichever generation is current and waits for its reconciler
// to return, leaving the process safe to close the shared store. After it
// begins, no new generation can be activated.
func (m *manager) shutdown() {
	m.mu.Lock()
	m.shuttingDown = true
	generation := m.current
	m.current = nil
	m.mu.Unlock()
	if generation == nil {
		return
	}
	generation.cancel()
	<-generation.done
	if err := generation.cloud.Close(); err != nil {
		m.logger.Warn("clouddrive close failed", "error", err)
	}
}

// build constructs a complete runtime generation for cfg: local filesystem,
// cloud client, reconciler coordinator, credentials, HTTP API, web UI, and the
// full mux. The generation owns its own reconciler context so that retiring it
// never cancels anything shared.
func (m *manager) build(ctx context.Context, cfg settings.Config) (*runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, err := fsafe.New(cfg.LocalRoot)
	if err != nil {
		return nil, fmt.Errorf("local root: %w", err)
	}
	cfg.LocalRoot = files.LocalRoot()

	if err := m.validatePersistedRoots(ctx, files); err != nil {
		return nil, err
	}

	cloud, err := clouddrive.Dial(cfg.CD2Address, cfg.CD2Username, cfg.CD2Password, cloudRPCTimeout, cfg.CD2Insecure)
	if err != nil {
		return nil, fmt.Errorf("clouddrive dial: %w", err)
	}
	// The generation context is created before the native handler because
	// the wait route observes its cancellation as the lifecycle shutdown
	// boundary. Any later build failure cancels it, so a partially built
	// generation can never leave waiters parked on a dead handler.
	generationContext, cancel := context.WithCancel(context.Background())
	built := false
	defer func() {
		if !built {
			cancel()
			_ = cloud.Close()
		}
	}()

	coord, err := reconcile.New(reconcile.Config{
		Owner:          workerOwner(),
		LeaseDuration:  reconcileLeaseDuration,
		PollInterval:   15 * time.Second,
		OfflineTimeout: cfg.OfflineTimeout,
		CopyTimeout:    cfg.CopyTimeout,
		VerifyTimeout:  cfg.VerifyTimeout,
		WorkerCount:    4,
	}, m.store, cloud, files, m.clock, m.logger)
	if err != nil {
		return nil, fmt.Errorf("reconciler: %w", err)
	}

	credentials, err := creds.New(m.store)
	if err != nil {
		return nil, fmt.Errorf("credentials: %w", err)
	}
	limits := metadataLimits()
	service, err := submission.New(submission.Config{
		CloudRoot: cfg.CloudRoot, LocalRoot: cfg.LocalRoot,
		TorrentLimits: limits,
	}, m.store, m.clock, coord, files)
	if err != nil {
		return nil, fmt.Errorf("submission service: %w", err)
	}
	api, err := httpapi.New(httpapi.Config{
		CloudRoot: cfg.CloudRoot, LocalRoot: cfg.LocalRoot,
		TorrentLimits: limits, MaxRequestBytes: int64(limits.MaxInputBytes) + 64<<10,
	}, credentials, m.store, m.sessions, m.clock, coord, files, service, m.store)
	if err != nil {
		return nil, fmt.Errorf("httpapi: %w", err)
	}
	nativeAuth, err := nativeapi.NewAuth(m.store)
	if err != nil {
		return nil, fmt.Errorf("nativeapi auth: %w", err)
	}
	native, err := nativeapi.NewHandler(nativeapi.Config{
		MaxRequestBytes: int64(limits.MaxInputBytes) + 64<<10,
		TorrentLimits:   limits,
		Shutdown:        generationContext.Done(),
	}, service, m.store, m.store.EventSignal())
	uiConfig := web.Config{
		CloudRoot: cfg.CloudRoot,
		LocalRoot: cfg.LocalRoot,
	}
	if err != nil {
		return nil, fmt.Errorf("nativeapi: %w", err)
	}
	if m.logReader != nil {
		uiConfig.LogReader = *m.logReader
	}
	ui, err := web.New(uiConfig, credentials, m.store, m.sessions, m.clock, coord, cloud, files, web.SettingsDeps{
		Store:   m.store,
		Tokens:  m.store,
		QBTKeys: m.store,
		Dial:    nil,
		Apply:   m.Apply,
	}, m.store)
	if err != nil {
		return nil, fmt.Errorf("web: %w", err)
	}

	health := server.NewHandler(
		func(requestContext context.Context) error {
			return m.store.PingContext(requestContext)
		},
		func(requestContext context.Context) error {
			return m.store.Ready(requestContext, cfg.LocalRoot)
		},
		cloud.Check,
	)
	root := http.NewServeMux()
	root.Handle("/healthz", health)
	root.Handle("/readyz", health)
	root.Handle("/api/v2/", api)
	root.Handle("/api/v1/", nativeAuth.Middleware(native))
	root.Handle("/setup", http.RedirectHandler("/", http.StatusSeeOther))
	root.Handle("/", ui)

	built = true
	return &runtime{
		cfg:     cfg,
		handler: root,
		cloud:   cloud,
		coord:   coord,
		ctx:     generationContext,
		cancel:  cancel,
		done:    make(chan struct{}),
	}, nil
}

func (m *manager) validatePersistedRoots(ctx context.Context, files *fsafe.Verifier) error {
	categories, err := m.store.ListCategories(ctx)
	if err != nil {
		return fmt.Errorf("persisted root preflight categories: %w", err)
	}
	for _, category := range categories {
		if _, err := files.ValidatePersistedRoot(category.SavePath); err != nil {
			return fmt.Errorf("persisted root preflight category %q save path %q: %w", category.Name, category.SavePath, err)
		}
	}

	downloads, err := m.store.ListAllDownloads(ctx)
	if err != nil {
		return fmt.Errorf("persisted root preflight downloads: %w", err)
	}
	for _, download := range downloads {
		if download.State == domain.StateDeleted && (download.ContentPath == "" || download.DeleteFilesRequested) {
			continue
		}
		if _, err := files.ValidatePersistedRoot(download.SavePath); err != nil {
			return fmt.Errorf("persisted root preflight download %q save path %q: %w", download.Hash, download.SavePath, err)
		}
	}
	return nil
}

// setupModeMux serves the HTTP surface while setup has not completed: the
// wizard and its static/localization routes, fixed liveness and readiness
// probes, a 503 API placeholder, and a redirect to the wizard for everything
// else.
func setupModeMux(setup http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/healthz", plainTextHandler(http.StatusOK, "ok\n"))
	mux.Handle("/readyz", plainTextHandler(http.StatusServiceUnavailable, "not ready\n"))
	mux.Handle("/api/v2/", plainTextHandler(http.StatusServiceUnavailable, "setup in progress\n"))
	mux.Handle("/api/v1/", http.HandlerFunc(setupAPIUnavailable))
	mux.Handle("/setup", setup)
	mux.Handle("/setup/", setup)
	mux.Handle("/lang", setup)
	mux.Handle("/static/", setup)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
	})
	return mux
}

// setupAPIUnavailable answers every native API request with the stable setup
// placeholder. Setup mode runs no authentication: the token store is not
// populated until setup completes.
func setupAPIUnavailable(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(setupIncompleteBody))
}

// setupIncompleteBody is the fixed JSON body for /api/v1/* during setup.
const setupIncompleteBody = "{\"error\":{\"code\":\"setup_incomplete\",\"message\":\"Setup is incomplete\"}}\n"

func plainTextHandler(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}
