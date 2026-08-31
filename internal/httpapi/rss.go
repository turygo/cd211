package httpapi

import "net/http"

func (h *handler) rssItems(w http.ResponseWriter, r *http.Request) {
	if _, ok := qbtParams(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}
func (h *handler) rssMatchingArticles(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireQBTParams(w, r, "ruleName"); ok {
		writeJSON(w, http.StatusOK, map[string]any{})
	}
}
func (h *handler) rssRules(w http.ResponseWriter, r *http.Request) {
	if _, ok := qbtParams(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}
func (h *handler) rssAddFeed(w http.ResponseWriter, r *http.Request)    { qbtNoop(w, r, "url", "path") }
func (h *handler) rssAddFolder(w http.ResponseWriter, r *http.Request)  { qbtNoop(w, r, "path") }
func (h *handler) rssMarkAsRead(w http.ResponseWriter, r *http.Request) { qbtNoop(w, r, "itemPath") }
func (h *handler) rssMoveItem(w http.ResponseWriter, r *http.Request) {
	qbtNoop(w, r, "itemPath", "destPath")
}
func (h *handler) rssRefreshItem(w http.ResponseWriter, r *http.Request) { qbtNoop(w, r, "itemPath") }
func (h *handler) rssRemoveItem(w http.ResponseWriter, r *http.Request)  { qbtNoop(w, r, "path") }
func (h *handler) rssRemoveRule(w http.ResponseWriter, r *http.Request)  { qbtNoop(w, r, "ruleName") }
func (h *handler) rssRenameRule(w http.ResponseWriter, r *http.Request) {
	qbtNoop(w, r, "ruleName", "newRuleName")
}
func (h *handler) rssSetFeedURL(w http.ResponseWriter, r *http.Request) { qbtNoop(w, r, "path", "url") }
func (h *handler) rssSetRule(w http.ResponseWriter, r *http.Request) {
	qbtNoop(w, r, "ruleName", "ruleDef")
}
