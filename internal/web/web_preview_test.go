package web

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/domain"
)

const webPreviewAddressEnv = "CD211_WEB_PREVIEW_ADDRESS"

func setPreviewSession(r *http.Request, sid string) {
	cookies := r.Cookies()
	r.Header.Del("Cookie")
	for _, cookie := range cookies {
		if cookie.Name != "CD211_SESSION" {
			r.AddCookie(cookie)
		}
	}
	r.AddCookie(&http.Cookie{Name: "CD211_SESSION", Value: sid, Path: "/"})
}

func TestSetPreviewSessionReplacesSessionCookie(t *testing.T) {
	request, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatalf("NewRequest(): %v", err)
	}
	request.AddCookie(&http.Cookie{Name: "lang", Value: "zh"})
	request.AddCookie(&http.Cookie{Name: "CD211_SESSION", Value: "stale"})

	setPreviewSession(request, "preview")

	if sid, err := request.Cookie("CD211_SESSION"); err != nil || sid.Value != "preview" {
		t.Fatalf("CD211_SESSION = (%v, %v), want preview", sid, err)
	}
	if lang, err := request.Cookie("lang"); err != nil || lang.Value != "zh" {
		t.Fatalf("lang = (%v, %v), want zh", lang, err)
	}
}

// TestWebPreview serves the real Web UI with an authenticated temporary test
// fixture when explicitly enabled. Normal test runs skip it.
func TestWebPreview(t *testing.T) {
	address := os.Getenv(webPreviewAddressEnv)
	if address == "" {
		t.Skip(webPreviewAddressEnv + " is not set")
	}

	fixture := newWebFixture(t)
	fixture.seedCategory("movies", true)
	fixture.seedDownload("1", domain.StateWaitingOffline, func(download *domain.Download) {
		download.WorkspacePath = filepath.Join(download.SavePath, ".cd211", download.Hash)
	})
	fixture.seedDownload("2", domain.StateWaitingCopy, nil)
	fixture.seedDownload("3", domain.StateCompleted, func(download *domain.Download) {
		download.WorkspacePath = filepath.Join(download.SavePath, ".cd211", download.Hash)
		download.ContentPath = filepath.Join(download.WorkspacePath, download.Name)
	})
	fixture.seedDownload("4", domain.StateFailed, func(download *domain.Download) {
		download.LastError = "The upstream task failed."
	})

	preview := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setPreviewSession(r, fixture.sid)
		fixture.handler.ServeHTTP(w, r)
	})
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("listen on %s: %v", address, err)
	}
	server := &http.Server{
		Handler:           preview,
		ReadHeaderTimeout: 5 * time.Second,
	}

	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(listener)
	}()
	t.Logf("Web preview ready at http://%s", listener.Addr())

	select {
	case err := <-serveResult:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("serve Web preview: %v", err)
		}
	case <-signalContext.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			t.Errorf("shut down Web preview: %v", err)
		}
		if err := <-serveResult; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serve Web preview after shutdown: %v", err)
		}
	}
}
