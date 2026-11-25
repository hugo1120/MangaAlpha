package httpserver

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/extmeta"
)

func (s *Server) metadataRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.requireMetadata)
		r.Get("/metadata/overview", s.metadataOverview)
		r.Post("/metadata/repos", s.subscribeMetadataRepo)
		r.Post("/metadata/repos/{repoID}/refresh", s.refreshMetadataRepo)
		r.Delete("/metadata/repos/{repoID}", s.deleteMetadataRepo)
		r.Post("/metadata/sources", s.addMetadataSource)
		r.Put("/metadata/sources/{sourceID}", s.updateMetadataSource)
		r.Delete("/metadata/sources/{sourceID}", s.deleteMetadataSource)
		r.Post("/metadata/sources/{sourceID}/sync", s.startMetadataSync)
		r.Get("/metadata/syncs", s.listMetadataSyncs)
		r.Put("/libraries/{libraryID}/metadata-sources", s.setLibraryMetadataSources)
		r.Post("/metadata/scrape", s.startMetadataScrape)
		r.Get("/metadata/scrape-runs", s.listMetadataScrapes)
		r.Post("/metadata/scrape-runs/{runID}/cancel", s.cancelMetadataScrape)
		r.Get("/series/{seriesID}/metadata/candidates", s.metadataCandidates)
		r.Post("/series/{seriesID}/metadata/bind", s.bindMetadata)
		r.Delete("/series/{seriesID}/metadata/bind", s.clearMetadataBinding)
		r.Get("/metadata/sources/{sourceID}/entries/{entryID}/cover", s.metadataEntryCover)
		r.Post("/metadata/mappings/export", s.exportCommunityMappings)
	})
}

func (s *Server) requireMetadata(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		if s.metadata == nil {
			writeError(w, http.StatusServiceUnavailable, "METADATA_UNAVAILABLE", "Metadata service is unavailable.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) metadataError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, extmeta.ErrEntryNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Metadata item not found.")
	case errors.Is(err, extmeta.ErrInvalidConfig), errors.Is(err, extmeta.ErrSSRFBlocked):
		writeError(w, http.StatusBadRequest, "INVALID_METADATA_CONFIG", "Invalid metadata configuration.")
	case errors.Is(err, extmeta.ErrNoReadySources):
		writeError(w, http.StatusBadRequest, "NO_READY_METADATA_SOURCES", err.Error())
	case errors.Is(err, extmeta.ErrBusy):
		writeError(w, http.StatusConflict, "METADATA_BUSY", "A metadata task is already running.")
	case errors.Is(err, extmeta.ErrUnavailable), errors.Is(err, extmeta.ErrCircuitOpen):
		writeError(w, http.StatusServiceUnavailable, "METADATA_UNAVAILABLE", "Metadata source is unavailable.")
	case errors.Is(err, extmeta.ErrRemoteMetadata), errors.Is(err, extmeta.ErrNegativeCached):
		writeError(w, http.StatusBadGateway, "METADATA_UPSTREAM_FAILED", "Could not fetch upstream metadata.")
	case errors.Is(err, context.Canceled):
		writeError(w, 499, "REQUEST_CANCELED", "Request canceled.")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "REQUEST_TIMEOUT", "Request timed out.")
	default:
		writeError(w, http.StatusInternalServerError, "METADATA_FAILED", "Metadata operation failed.")
	}
}

func (s *Server) metadataResult(w http.ResponseWriter, status int, data any, err error) {
	if err != nil {
		s.metadataError(w, err)
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, data)
}

func (s *Server) metadataOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repos, err := s.store.ListMetadataRepos(ctx)
	if err != nil {
		s.metadataError(w, err)
		return
	}
	sources, err := s.store.ListMetadataSources(ctx, "")
	if err != nil {
		s.metadataError(w, err)
		return
	}
	chains, err := s.store.ListAllLibraryMetadataChains(ctx)
	if err != nil {
		s.metadataError(w, err)
		return
	}
	syncs, online := s.metadata.Syncs(), s.metadata.OnlineEnrichStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"recommendedRepoUrl": extmeta.RecommendedRepoURL,
		"repos":              repos, "sources": sources, "libraryChains": chains,
		"syncs": syncs, "activeSyncs": syncs, "scrapeRuns": s.metadata.ScrapeRuns(),
		"onlineEnrich": online, "onlineEnrichStatus": online,
	})
}

func (s *Server) subscribeMetadataRepo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if decodeJSON(w, r, &req) {
		repo, err := s.metadata.SubscribeRepo(r.Context(), req.URL)
		s.metadataResult(w, http.StatusCreated, repo, err)
	}
}

func (s *Server) refreshMetadataRepo(w http.ResponseWriter, r *http.Request) {
	repo, err := s.metadata.RefreshRepo(r.Context(), chi.URLParam(r, "repoID"))
	s.metadataResult(w, http.StatusOK, repo, err)
}

func (s *Server) deleteMetadataRepo(w http.ResponseWriter, r *http.Request) {
	s.metadataResult(w, http.StatusNoContent, nil, s.metadata.DeleteRepo(r.Context(), chi.URLParam(r, "repoID")))
}

func (s *Server) addMetadataSource(w http.ResponseWriter, r *http.Request) {
	req := database.MetadataSource{Enabled: true}
	if decodeJSON(w, r, &req) {
		src, err := s.metadata.AddCustomSource(r.Context(), req)
		s.metadataResult(w, http.StatusCreated, src, err)
	}
}

func (s *Server) updateMetadataSource(w http.ResponseWriter, r *http.Request) {
	var req extmeta.SourceConfig
	if decodeJSON(w, r, &req) {
		src, err := s.metadata.UpdateSourceConfig(r.Context(), chi.URLParam(r, "sourceID"), req)
		s.metadataResult(w, http.StatusOK, src, err)
	}
}

func (s *Server) deleteMetadataSource(w http.ResponseWriter, r *http.Request) {
	s.metadataResult(w, http.StatusNoContent, nil, s.metadata.DeleteSource(r.Context(), chi.URLParam(r, "sourceID")))
}

func (s *Server) startMetadataSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Force bool `json:"force"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	run, err := s.metadata.StartSourceSync(r.Context(), chi.URLParam(r, "sourceID"), req.Force)
	s.metadataResult(w, http.StatusAccepted, run, err)
}

func (s *Server) listMetadataSyncs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"syncs": s.metadata.Syncs()})
}

func (s *Server) setLibraryMetadataSources(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceIDs []string `json:"sourceIds"`
	}
	if decodeJSON(w, r, &req) {
		chain, err := s.metadata.SetLibrarySources(r.Context(), chi.URLParam(r, "libraryID"), req.SourceIDs)
		s.metadataResult(w, http.StatusOK, map[string]any{"sources": chain}, err)
	}
}

func (s *Server) startMetadataScrape(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LibraryID   string `json:"libraryId"`
		Mode        string `json:"mode"`
		AllowOnline bool   `json:"allowOnline"`
	}
	if decodeJSON(w, r, &req) {
		run, err := s.metadata.StartMultiScrape(r.Context(), req.LibraryID, req.Mode, req.AllowOnline)
		s.metadataResult(w, http.StatusAccepted, run, err)
	}
}

func (s *Server) listMetadataScrapes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"runs": s.metadata.ScrapeRuns(), "onlineEnrich": s.metadata.OnlineEnrichStatus()})
}

func (s *Server) cancelMetadataScrape(w http.ResponseWriter, r *http.Request) {
	s.metadataResult(w, http.StatusNoContent, nil, s.metadata.CancelScrape(chi.URLParam(r, "runID")))
}

func (s *Server) metadataCandidates(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	sourceID := query.Get("sourceId")
	if sourceID == "" {
		sourceID = "bangumi"
	}
	hits, err := s.metadata.SearchCandidates(r.Context(), chi.URLParam(r, "seriesID"), sourceID, query.Get("q"))
	s.metadataResult(w, http.StatusOK, hits, err)
}

func (s *Server) bindMetadata(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceID string `json:"sourceId"`
		EntryID  string `json:"entryId"`
	}
	if decodeJSON(w, r, &req) {
		meta, err := s.metadata.BindSeriesExternal(r.Context(), chi.URLParam(r, "seriesID"), req.SourceID, req.EntryID, "manual")
		s.metadataResult(w, http.StatusOK, meta, err)
	}
}

func (s *Server) clearMetadataBinding(w http.ResponseWriter, r *http.Request) {
	meta, err := s.metadata.ClearSeriesExternal(r.Context(), chi.URLParam(r, "seriesID"))
	s.metadataResult(w, http.StatusOK, meta, err)
}

func (s *Server) metadataEntryCover(w http.ResponseWriter, r *http.Request) {
	data, mediaType, err := s.metadata.EntryCover(r.Context(), chi.URLParam(r, "sourceID"), chi.URLParam(r, "entryID"))
	if err != nil {
		s.metadataError(w, err)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (s *Server) exportCommunityMappings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		database.MappingExportFilter
		Filter  *database.MappingExportFilter `json:"filter"`
		Preview bool                          `json:"preview"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Filter != nil {
		req.MappingExportFilter = *req.Filter
	}
	req.Preview = req.Preview || strings.EqualFold(r.URL.Query().Get("preview"), "true")
	export, err := s.metadata.ExportCommunityMappings(r.Context(), req.MappingExportFilter, req.Preview)
	if err != nil || req.Preview {
		s.metadataResult(w, http.StatusOK, export, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="metadata-mappings.ndjson"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(export.NDJSON)))
	_, _ = w.Write(export.NDJSON)
}
