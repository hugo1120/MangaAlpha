package httpserver

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) unlockBookArchiveHandler(w http.ResponseWriter, r *http.Request) {
	bookID := chi.URLParam(r, "bookID")
	book, err := s.store.BookByID(r.Context(), bookID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Book not found.")
			return
		}
		s.writeDBError(w, r, err)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "Invalid request body.")
		return
	}
	if req.Password == "" {
		writeError(w, http.StatusBadRequest, "EMPTY_PASSWORD", "请输入解压密码。")
		return
	}
	pageCount, err := s.archive.VerifyAndBindBookPassword(r.Context(), book, req.Password)
	if err != nil {
		s.archiveError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"bookId":    book.ID,
		"pageCount": pageCount,
	})
}

func (s *Server) listArchivePasswordsHandler(w http.ResponseWriter, r *http.Request) {
	groups, err := s.store.ListArchivePasswordGroups(r.Context())
	if err != nil {
		s.writeDBError(w, r, err)
		return
	}
	totalBindings := 0
	for _, g := range groups {
		totalBindings += len(g.Bindings)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"groups":         groups,
		"totalPasswords": len(groups),
		"totalBindings":  totalBindings,
	})
}

func (s *Server) deleteArchivePasswordHandler(w http.ResponseWriter, r *http.Request) {
	passwordID := chi.URLParam(r, "passwordID")
	affected, err := s.store.DeleteArchivePassword(r.Context(), passwordID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Password entry not found.")
			return
		}
		s.writeDBError(w, r, err)
		return
	}
	if err := s.cache.DeleteBookPages(r.Context(), affected); err != nil {
		s.logger.Warn("purge decrypted page cache after password delete", "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":       true,
		"affectedBooks": len(affected),
	})
}

func (s *Server) deleteArchivePasswordBindingHandler(w http.ResponseWriter, r *http.Request) {
	bookID := chi.URLParam(r, "bookID")
	affected, err := s.store.DeleteArchivePasswordBinding(r.Context(), bookID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Password binding not found.")
			return
		}
		s.writeDBError(w, r, err)
		return
	}
	if err := s.cache.DeleteBookPages(r.Context(), affected); err != nil {
		s.logger.Warn("purge decrypted page cache after binding delete", "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"deleted":       true,
		"affectedBooks": len(affected),
	})
}

func (s *Server) clearArchivePasswordsHandler(w http.ResponseWriter, r *http.Request) {
	affected, err := s.store.ClearAllArchivePasswords(r.Context())
	if err != nil {
		s.writeDBError(w, r, err)
		return
	}
	if err := s.cache.DeleteBookPages(r.Context(), affected); err != nil {
		s.logger.Warn("purge decrypted page cache after clearing passwords", "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cleared":       true,
		"affectedBooks": len(affected),
	})
}
