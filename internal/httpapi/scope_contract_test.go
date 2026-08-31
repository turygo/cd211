package httpapi

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"

	"github.com/turygo/cd211/internal/fsafe"
)

type scopeFilesystem struct{ listErr error }

func (f scopeFilesystem) Verify(string, fsafe.ExpectedContent) (fsafe.VerifiedContent, error) {
	return fsafe.VerifiedContent{}, nil
}
func (f scopeFilesystem) ResolveSaveRoot(string) (string, bool, error)   { return "", false, nil }
func (f scopeFilesystem) PrepareSaveRoot(string) (string, error)         { return "", nil }
func (f scopeFilesystem) ListDirectory(string, string) ([]string, error) { return nil, f.listErr }

func TestScopeAppStatusContracts(t *testing.T) {
	for _, test := range []struct {
		name, target  string
		filesystemErr error
		want          int
	}{
		{name: "missing directory", target: "/api/v2/app/getDirectoryContent?dirPath=%2Flocal%2Fmissing", filesystemErr: fs.ErrNotExist, want: http.StatusNotFound},
		{name: "file target", target: "/api/v2/app/getDirectoryContent?dirPath=%2Flocal%2Ffile", filesystemErr: syscall.ENOTDIR, want: http.StatusNotFound},
		{name: "unsafe path", target: "/api/v2/app/getDirectoryContent?dirPath=%2Flocal%2F..%2Foutside", filesystemErr: fsafe.ErrUnsafePath, want: http.StatusBadRequest},
		{name: "invalid visibility", target: "/api/v2/app/getDirectoryContent?dirPath=%2Flocal%2Fdir&mode=other", filesystemErr: fsafe.ErrInvalidVisibility, want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &handler{filesystem: scopeFilesystem{listErr: test.filesystemErr}}
			req := httptest.NewRequest(http.MethodGet, test.target, nil)
			response := httptest.NewRecorder()
			h.getDirectoryContent(response, req)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d (%q)", response.Code, test.want, response.Body.String())
			}
		})
	}

	for _, test := range []struct {
		name, target string
		want         int
	}{
		{name: "missing iface", target: "/api/v2/app/networkInterfaceAddressList", want: http.StatusBadRequest},
		{name: "empty iface", target: "/api/v2/app/networkInterfaceAddressList?iface=", want: http.StatusOK},
		{name: "repeated iface", target: "/api/v2/app/networkInterfaceAddressList?iface=eth0&iface=eth1", want: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &handler{}
			req := httptest.NewRequest(http.MethodGet, test.target, nil)
			response := httptest.NewRecorder()
			h.networkInterfaceAddressList(response, req)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}

func TestScopeSearchSyntheticIDContracts(t *testing.T) {
	for _, route := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
	}{
		{name: "status", handler: (&handler{}).searchStatus},
		{name: "delete", handler: (&handler{}).searchDelete},
		{name: "stop", handler: (&handler{}).searchStop},
		{name: "results", handler: (&handler{}).searchResults},
	} {
		for _, test := range []struct {
			name, id string
			want     int
		}{
			{name: "missing", want: http.StatusBadRequest},
			{name: "noninteger", id: "nope", want: http.StatusNotFound},
			{name: "unknown", id: "1", want: http.StatusNotFound},
			{name: "synthetic", id: "0", want: http.StatusOK},
		} {
			t.Run(route.name+"/"+test.name, func(t *testing.T) {
				target := "/api/v2/search/" + route.name
				if test.name != "missing" {
					target += "?id=" + url.QueryEscape(test.id)
				}
				req := httptest.NewRequest(http.MethodGet, target, nil)
				response := httptest.NewRecorder()
				route.handler(response, req)
				if response.Code != test.want {
					t.Fatalf("status = %d, want %d (%q)", response.Code, test.want, response.Body.String())
				}
			})
		}
	}
}

func TestScopeSyncTorrentPeersHashContracts(t *testing.T) {
	for _, test := range []struct {
		name, target string
		want         int
	}{
		{name: "missing", target: "/api/v2/sync/torrentPeers", want: http.StatusNotFound},
		{name: "invalid", target: "/api/v2/sync/torrentPeers?hash=bad", want: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &handler{}
			req := httptest.NewRequest(http.MethodGet, test.target, nil)
			response := httptest.NewRecorder()
			h.syncTorrentPeers(response, req)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
}

func TestScopeAppUnexpectedDirectoryErrorIsInternal(t *testing.T) {
	h := &handler{filesystem: scopeFilesystem{listErr: errors.New("storage unavailable")}}
	req := httptest.NewRequest(http.MethodGet, "/api/v2/app/getDirectoryContent?dirPath=%2Flocal%2Fdir", strings.NewReader(""))
	response := httptest.NewRecorder()
	h.getDirectoryContent(response, req)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
}
