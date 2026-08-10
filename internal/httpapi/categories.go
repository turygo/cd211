package httpapi

import (
	"errors"
	"net/http"
	"path"
	"path/filepath"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/store"
	"github.com/turygo/cd211/internal/submission"
)

func (h *handler) categories(w http.ResponseWriter, r *http.Request) {
	rows, err := h.repo.ListCategories(r.Context())
	if err != nil {
		internalError(w)
		return
	}
	result := make(map[string]categoryView, len(rows))
	for _, category := range rows {
		result[category.Name] = categoryView{Name: category.Name, SavePath: category.SavePath}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *handler) createCategory(w http.ResponseWriter, r *http.Request) {
	form, ok := parseURLEncodedForm(w, r, formLimit)
	if !ok {
		return
	}
	rawName, present := exactlyOne(form["category"])
	name, valid := submission.CanonicalCategory(rawName, false)
	if !present || !valid {
		badRequest(w)
		return
	}

	savePath := filepath.Join(h.config.LocalRoot, name)
	if values, exists := form["savePath"]; exists {
		explicit, one := exactlyOne(values)
		if !one || !filepath.IsAbs(explicit) || filepath.Clean(explicit) != explicit {
			badRequest(w)
			return
		}
		savePath = explicit
	}
	resolvedSavePath, _, err := h.filesystem.ResolveSaveRoot(savePath)
	if err != nil {
		badRequest(w)
		return
	}
	now := h.now()
	category := domain.Category{
		Name: name, CloudPath: path.Join(h.config.CloudRoot, name), SavePath: resolvedSavePath,
		Enabled: false, CreatedAt: now, UpdatedAt: now,
	}
	if _, err = h.repo.UpsertCategory(r.Context(), category); err != nil {
		if errors.Is(err, store.ErrDestinationConflict) {
			badRequest(w)
			return
		}
		internalError(w)
		return
	}
	preparedSavePath, err := h.filesystem.PrepareSaveRoot(resolvedSavePath)
	if err != nil || preparedSavePath != resolvedSavePath {
		badRequest(w)
		return
	}
	category.Enabled = true
	if _, err = h.repo.UpsertCategory(r.Context(), category); err != nil {
		if errors.Is(err, store.ErrDestinationConflict) {
			badRequest(w)
			return
		}
		internalError(w)
		return
	}
	w.WriteHeader(http.StatusOK)
}
