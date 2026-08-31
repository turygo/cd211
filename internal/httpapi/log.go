package httpapi

import "net/http"

// qbtParams applies the same form gate to every form-based qB endpoint while
// still allowing read actions over GET query parameters.
func qbtParams(w http.ResponseWriter, r *http.Request) (map[string][]string, bool) {
	if r.Method == http.MethodGet {
		return r.URL.Query(), true
	}
	return parseURLEncodedForm(w, r, formLimit)
}

func requireQBTParams(w http.ResponseWriter, r *http.Request, names ...string) (map[string][]string, bool) {
	params, ok := qbtParams(w, r)
	if !ok {
		return nil, false
	}
	for _, name := range names {
		if _, present := params[name]; !present {
			badRequest(w)
			return nil, false
		}
	}
	return params, true
}
func qbtNoop(w http.ResponseWriter, r *http.Request, names ...string) {
	if _, ok := requireQBTParams(w, r, names...); ok {
		w.WriteHeader(http.StatusOK)
	}
}

func (h *handler) logMain(w http.ResponseWriter, r *http.Request) {
	if _, ok := qbtParams(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, []any{})
}
func (h *handler) logPeers(w http.ResponseWriter, r *http.Request) {
	if _, ok := qbtParams(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, []any{})
}
