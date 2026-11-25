package extmeta

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/database"
)

type ScrapeRun struct {
	ID            string `json:"id"`
	LibraryID     string `json:"libraryId"`
	LibraryName   string `json:"libraryName,omitempty"`
	CurrentSeries string `json:"currentSeries,omitempty"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
	AllowOnline   bool   `json:"allowOnline"`
	StartedAt     string `json:"startedAt"`
	CompletedAt   string `json:"completedAt,omitempty"`
	TotalSeries   int    `json:"totalSeries"`
	Processed     int    `json:"processedSeries"`
	Matched       int    `json:"matchedCount"`
	Mapped        int    `json:"mappingCount"`
	Unmatched     int    `json:"unmatchedCount"`
	Skipped       int    `json:"skippedCount"`
	Failed        int    `json:"failedCount"`
	Error         string `json:"error,omitempty"`
}

type scrapeSeries struct {
	series    database.Series
	coverSHA1 string
	bound     bool
}

type scrapePlan struct {
	sources  map[string]database.MetadataSource
	chains   map[string][]database.LibraryMetadataSource
	mappings *MappingIndex
	bySchema map[string][]string
	online   []database.MetadataSource
}

func (m *Manager) StartMultiScrape(ctx context.Context, libraryID, mode string, allowOnline bool) (ScrapeRun, error) {
	if err := checkAdmin(ctx); err != nil {
		return ScrapeRun{}, err
	}
	libraryID = strings.TrimSpace(libraryID)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = bangumi.ScrapeModeIncremental
	}
	switch mode {
	case bangumi.ScrapeModeIncremental, bangumi.ScrapeModeRetryUnmatched, bangumi.ScrapeModeForce:
	default:
		return ScrapeRun{}, ErrInvalidConfig
	}
	libraryName := "全部未隐藏书库"
	if libraryID != "" {
		lib, err := m.store.LibraryBasic(ctx, libraryID)
		if err != nil {
			return ScrapeRun{}, err
		}
		if !lib.Enabled || lib.UnionParentID != "" {
			return ScrapeRun{}, ErrUnavailable
		}
		libraryName = lib.Name
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ScrapeRun{}, ErrUnavailable
	}
	if m.scrapeCancel != nil {
		return ScrapeRun{}, ErrBusy
	}
	jobCtx, cancel := context.WithCancel(m.ctx)
	m.scrapeCancel = cancel
	run := ScrapeRun{
		ID: newRunID(), LibraryID: libraryID, LibraryName: libraryName, Mode: mode, AllowOnline: allowOnline,
		Status: "running", StartedAt: timestamp(),
	}
	m.scrapes = append(m.scrapes, run)
	if len(m.scrapes) > runHistoryLimit {
		m.scrapes = slices.Delete(m.scrapes, 0, len(m.scrapes)-runHistoryLimit)
	}
	m.wg.Go(func() {
		run := run
		defer cancel()
		err := m.multiScrape(database.WithAutomaticMetadata(jobCtx), &run)
		m.mu.Lock()
		defer m.mu.Unlock()
		switch {
		case jobCtx.Err() != nil:
			run.Status = "canceled"
		case err != nil:
			run.Status, run.Error = "failed", "metadata scrape failed"
		default:
			run.Status = "success"
		}
		run.CompletedAt = timestamp()
		m.scrapes[len(m.scrapes)-1] = run
		m.scrapeCancel = nil
	})
	return run, nil
}

func (m *Manager) ScrapeRuns() []ScrapeRun {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := append([]ScrapeRun{}, m.scrapes...)
	slices.Reverse(out)
	return out
}

func (m *Manager) CancelScrape(runID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scrapeCancel == nil || m.scrapes[len(m.scrapes)-1].ID != runID {
		return sql.ErrNoRows
	}
	m.scrapeCancel()
	return nil
}

func (m *Manager) CancelLibrary(libraryID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scrapeCancel != nil && m.scrapes[len(m.scrapes)-1].LibraryID == libraryID {
		m.scrapeCancel()
	}
	// An all-library run checks visibility at each write and before online requests.
}

func (m *Manager) scrapePlan(ctx context.Context) (scrapePlan, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sources, err := m.store.ListMetadataSources(ctx, "")
	if err != nil {
		return scrapePlan{}, err
	}
	chains, err := m.store.ListAllLibraryMetadataChains(ctx)
	if err != nil {
		return scrapePlan{}, err
	}
	plan := scrapePlan{
		sources: make(map[string]database.MetadataSource, len(sources)),
		chains:  chains, mappings: m.mappingIndex, bySchema: make(map[string][]string),
	}
	for _, src := range sources {
		if !src.Enabled {
			continue
		}
		plan.sources[src.ID] = src
		if sidecar := m.sidecars[src.ID]; src.Ready && sidecar != nil {
			schema := sidecar.Schema()
			plan.bySchema[schema] = append(plan.bySchema[schema], src.ID)
		}
		if src.OnlineEnabled && src.OnlineAPIURL != "" && src.Kind == KindOfflineSQLite {
			plan.online = append(plan.online, src)
		}
	}
	return plan, nil
}

func (m *Manager) scrapeTargets(ctx context.Context, libraryID, mode string) ([]scrapeSeries, error) {
	query := `
SELECT s.id,s.library_id,s.name,s.relative_path,s.one_shot,
       coalesce(sm.ext_cover_url,''),
       (coalesce(sm.bgm_subject_id,0)>0 OR coalesce(sm.ext_entry_id,'')<>'')
FROM series s JOIN libraries l ON l.id=s.library_id
LEFT JOIN series_metadata sm ON sm.series_id=s.id
WHERE l.enabled=1 AND NOT EXISTS (SELECT 1 FROM union_layers ul WHERE ul.member_library_id=l.id)
  AND coalesce(sm.match_origin,'') NOT IN ('manual','agent')
  AND NOT (coalesce(sm.match_origin,'')='' AND (coalesce(sm.bgm_subject_id,0)>0 OR coalesce(sm.ext_entry_id,'')<>''))`
	var args []any
	if libraryID != "" {
		query += " AND s.library_id=?"
		args = append(args, libraryID)
	}
	switch mode {
	case bangumi.ScrapeModeIncremental:
		query += " AND coalesce(sm.scrape_status,'none')='none'"
	case bangumi.ScrapeModeRetryUnmatched:
		query += " AND coalesce(sm.scrape_status,'none')<>'matched'"
	}
	rows, err := m.store.DB().QueryContext(ctx, query+" ORDER BY s.library_id,s.id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []scrapeSeries
	for rows.Next() {
		var target scrapeSeries
		var coverURL string
		if err := rows.Scan(&target.series.ID, &target.series.LibraryID, &target.series.Name,
			&target.series.RelativePath, &target.series.OneShot, &coverURL, &target.bound); err != nil {
			return nil, err
		}
		target.coverSHA1 = ExtractThumbSHA1(coverURL)
		out = append(out, target)
	}
	return out, rows.Err()
}

func (m *Manager) multiScrape(ctx context.Context, run *ScrapeRun) error {
	plan, err := m.scrapePlan(ctx)
	if err != nil {
		return err
	}
	targets, err := m.scrapeTargets(ctx, run.LibraryID, run.Mode)
	if err != nil {
		return err
	}
	run.TotalSeries = len(targets)
	m.publishScrapeProgress(run)
	pending := make([]scrapeSeries, 0)
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		run.CurrentSeries = target.series.Name
		m.publishScrapeProgress(run)
		incoming, matched, err := m.matchOffline(ctx, target, plan)
		if err == nil && !matched && run.AllowOnline && len(plan.online) > 0 {
			pending = append(pending, target)
		} else {
			m.finishSeries(ctx, run, target, incoming, matched, err)
		}
	}
	return m.enrichOnline(ctx, run, plan.online, pending)
}

func (m *Manager) finishSeries(ctx context.Context, run *ScrapeRun, target scrapeSeries, incoming database.ScrapedMetadata, matched bool, err error) error {
	if err == nil {
		switch {
		case matched:
			_, err = m.store.ApplyScrapedMetadata(ctx, target.series, incoming)
		case !target.bound:
			inferred := bangumi.InferPathNSFW(target.series.Name, target.series.RelativePath)
			err = m.store.MarkSeriesScrapeStatusWithNSFW(ctx, target.series, database.ScrapeStatusUnmatched, 0, inferred)
		}
	}
	run.Processed++
	switch {
	case errors.Is(err, database.ErrMetadataChanged):
		run.Skipped++
	case err != nil:
		run.Failed++
	case matched:
		run.Matched++
		if incoming.MatchOrigin == "mapping" {
			run.Mapped++
		}
	default:
		run.Unmatched++
	}
	m.publishScrapeProgress(run)
	return err
}

func (m *Manager) publishScrapeProgress(run *ScrapeRun) {
	m.mu.Lock()
	m.scrapes[len(m.scrapes)-1] = *run
	m.mu.Unlock()
}

func (m *Manager) matchOffline(ctx context.Context, target scrapeSeries, plan scrapePlan) (database.ScrapedMetadata, bool, error) {
	series := target.series
	chain := plan.chains[series.LibraryID]
	if len(chain) == 0 {
		chain = []database.LibraryMetadataSource{{SourceID: TargetSourceBangumi, Enabled: true}}
	}
	parsed := bangumi.ParseSeriesWithPath(series.Name, series.RelativePath)
	var embedded *EmbeddedMeta
	var readArchive bool
	var sourceErr error
	for _, link := range chain {
		if !link.Enabled {
			continue
		}
		src := plan.sources[link.SourceID]
		if link.SourceID == TargetSourceBangumi || src.Kind == KindBangumiArchive && src.Ready {
			if m.bangumi == nil {
				continue
			}
			meta, found, err := m.bangumi.OfflineMatch(ctx, series)
			if found {
				meta.MatchOrigin = "auto"
				return meta, true, nil
			}
			if err != nil {
				sourceErr = err
			}
		}
		if src.Kind != KindOfflineSQLite || !src.Ready {
			continue
		}
		if !readArchive && m.opts.ArchiveMetaReader != nil {
			readArchive = true
			if book, err := m.store.FirstBookInSeries(ctx, series.ID); err == nil {
				embedded, _ = m.opts.ArchiveMetaReader(ctx, book)
			}
		}
		entry, confidence, err := m.matchSidecar(ctx, src.ID, parsed.Candidates, target.coverSHA1, embedded)
		if err != nil {
			sourceErr = err
			continue
		}
		if entry != nil {
			return m.externalMetadata(ctx, src, entry, "auto", confidence), true, nil
		}
	}
	meta, found, err := m.matchMapping(ctx, target.series, parsed, chain, plan)
	if found {
		return meta, true, nil
	}
	return meta, false, errors.Join(sourceErr, err)
}

func (m *Manager) matchMapping(ctx context.Context, series database.Series, parsed bangumi.ParsedQuery, chain []database.LibraryMetadataSource, plan scrapePlan) (database.ScrapedMetadata, bool, error) {
	key := firstNonEmpty(append(slices.Clone(parsed.Candidates), series.Name)...)
	authors := parsed.Authors
	if len(authors) == 0 {
		authors = []string{""}
	}
	for _, author := range authors {
		if rec, ok := plan.mappings.Lookup(key, series.Name, author, TargetSourceBangumi); ok && m.bangumi != nil {
			if id, err := strconv.ParseInt(rec.TargetID, 10, 64); err == nil && id > 0 {
				meta, found, err := m.bangumi.OfflineSubject(ctx, series, id)
				if found || err != nil {
					meta.MatchOrigin = "mapping"
					return meta, found, err
				}
			}
		}
		for _, schema := range []string{TargetSourceMetaV1, TargetSourceGalleryV1} {
			rec, ok := plan.mappings.Lookup(key, series.Name, author, schema)
			if !ok {
				continue
			}
			ids := make([]string, 0, len(plan.bySchema[schema]))
			for _, link := range chain {
				if link.Enabled && slices.Contains(plan.bySchema[schema], link.SourceID) {
					ids = append(ids, link.SourceID)
				}
			}
			for _, id := range plan.bySchema[schema] {
				if !slices.Contains(ids, id) {
					ids = append(ids, id)
				}
			}
			for _, id := range ids {
				entry, err := m.entry(ctx, id, rec.TargetID)
				if errors.Is(err, ErrEntryNotFound) || errors.Is(err, ErrUnavailable) {
					continue
				}
				if err != nil {
					return database.ScrapedMetadata{}, false, err
				}
				return m.externalMetadata(ctx, plan.sources[id], entry, "mapping", 1), true, nil
			}
		}
	}
	return database.ScrapedMetadata{}, false, nil
}

func (m *Manager) matchSidecar(ctx context.Context, id string, titles []string, sha1 string, embedded *EmbeddedMeta) (*Entry, float64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sidecar := m.sidecars[id]
	if sidecar == nil {
		return nil, 0, nil
	}
	if embedded != nil {
		if sidecar.Schema() == SchemaGalleryV1 {
			entryID, token, ok := directEntryID(embedded.ExternalURL)
			if !ok && embedded.ExternalURL == "" {
				entryID, token, ok = directEntryID(embedded.ExternalID)
			}
			if ok {
				entry, err := getEntryWithToken(ctx, sidecar, entryID, token)
				if !errors.Is(err, ErrEntryNotFound) {
					return entry, 1, err
				}
			}
		}
		titles = append([]string{embedded.Series, embedded.Title}, titles...)
	}
	if sha1 != "" {
		hits, err := sidecar.LookupByThumbSHA1(ctx, sha1)
		if err != nil {
			return nil, 0, err
		}
		if len(hits) == 1 {
			entry, err := sidecar.GetEntry(ctx, hits[0].EntryID)
			if !errors.Is(err, ErrEntryNotFound) {
				return entry, 1, err
			}
		}
	}
	for _, title := range titles {
		if title == "" {
			continue
		}
		hits, err := sidecar.SearchByTitle(ctx, title, 2)
		if err != nil {
			return nil, 0, err
		}
		if len(hits) > 0 && hits[0].Score >= 0.95 && (len(hits) == 1 || hits[0].Score-hits[1].Score >= 0.05) {
			entry, err := sidecar.GetEntry(ctx, hits[0].EntryID)
			if !errors.Is(err, ErrEntryNotFound) {
				return entry, hits[0].Score, err
			}
		}
	}
	return nil, 0, nil
}

var (
	numericEntryID   = regexp.MustCompile(`^[0-9]+$`)
	entryIDWithToken = regexp.MustCompile(`^([0-9]+)/([A-Za-z0-9_-]+)/?$`)
	galleryEntryPath = regexp.MustCompile(`(?:^|/)g/([0-9]+)/([A-Za-z0-9_-]+)(?:/|$)`)
)

func directEntryID(raw string) (id, token string, ok bool) {
	raw = strings.TrimSpace(raw)
	if numericEntryID.MatchString(raw) {
		return raw, "", true
	}
	if match := entryIDWithToken.FindStringSubmatch(raw); match != nil {
		return match[1], match[2], true
	}
	if u, err := url.Parse(raw); err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "") {
		if match := galleryEntryPath.FindStringSubmatch(u.Path); match != nil {
			return match[1], match[2], true
		}
	}
	return "", "", false
}

func getEntryWithToken(ctx context.Context, sidecar *SidecarStore, entryID, token string) (*Entry, error) {
	entry, err := sidecar.GetEntry(ctx, entryID)
	if err == nil && token != "" && entry.Token != "" && !strings.EqualFold(entry.Token, token) {
		return nil, ErrEntryNotFound
	}
	return entry, err
}

func (m *Manager) entry(ctx context.Context, sourceID, entryID string) (*Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if sidecar := m.sidecars[sourceID]; sidecar != nil {
		if id, token, ok := directEntryID(entryID); ok {
			return getEntryWithToken(ctx, sidecar, id, token)
		}
		return sidecar.GetEntry(ctx, entryID)
	}
	return nil, ErrUnavailable
}

type CandidateResult struct {
	Parsed     bangumi.ParsedQuery `json:"parsed"`
	SourceID   string              `json:"sourceId,omitempty"`
	Candidates any                 `json:"candidates"`
}

func (m *Manager) SearchCandidates(ctx context.Context, seriesID, sourceID, query string) (CandidateResult, error) {
	if err := checkAdmin(ctx); err != nil {
		return CandidateResult{}, err
	}
	series, err := m.store.SeriesByIDAny(ctx, seriesID, false)
	if err != nil {
		return CandidateResult{}, err
	}
	if sourceID == TargetSourceBangumi {
		if m.bangumi == nil {
			return CandidateResult{}, ErrUnavailable
		}
		hits, parsed, err := m.bangumi.SearchCandidates(ctx, series, query)
		if hits == nil {
			hits = []bangumi.SubjectCandidate{}
		}
		return CandidateResult{Parsed: parsed, SourceID: sourceID, Candidates: hits}, err
	}
	src, err := m.store.GetMetadataSource(ctx, sourceID)
	if err != nil {
		return CandidateResult{}, err
	}
	if !src.Enabled || !src.Ready || src.Kind != KindOfflineSQLite {
		return CandidateResult{}, ErrUnavailable
	}
	parsed := bangumi.ParseSeriesWithPath(series.Name, series.RelativePath)
	hits := make([]*Entry, 0)
	dict := m.sourceTagDictionary(ctx, src.TagDictSourceID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	sidecar := m.sidecars[sourceID]
	if sidecar == nil {
		return CandidateResult{}, ErrUnavailable
	}
	query = strings.TrimSpace(query)
	if id, token, ok := directEntryID(query); ok {
		entry, err := getEntryWithToken(ctx, sidecar, id, token)
		if err == nil {
			hits = append(hits, entry)
		} else if !errors.Is(err, ErrEntryNotFound) {
			return CandidateResult{}, err
		}
	} else {
		if query != "" {
			parsed = bangumi.ParseSeriesName(query)
		}
		names := append(slices.Clone(parsed.Candidates), firstNonEmpty(query, series.Name))
		seen := make(map[string]bool)
		for _, name := range names {
			matches, err := sidecar.SearchByTitle(ctx, name, 20)
			if err != nil {
				return CandidateResult{}, err
			}
			for _, hit := range matches {
				if seen[hit.EntryID] || len(hits) >= 20 {
					continue
				}
				entry, err := sidecar.GetEntry(ctx, hit.EntryID)
				if err != nil {
					return CandidateResult{}, err
				}
				hits = append(hits, entry)
				seen[hit.EntryID] = true
			}
		}
	}
	for i, hit := range hits {
		entry := *hit
		entry.SourceID = sourceID
		entry.Tags = slices.Clone(hit.Tags)
		if dict != nil {
			entry.Tags = dict.TranslateList(entry.Tags, TagDisplayMode(src.TagDisplayMode))
		}
		hits[i] = &entry
	}
	return CandidateResult{Parsed: parsed, SourceID: sourceID, Candidates: hits}, nil
}

func (m *Manager) BindSeriesExternal(ctx context.Context, seriesID, sourceID, entryID, origin string) (database.SeriesMetadata, error) {
	if err := checkAdmin(ctx); err != nil {
		return database.SeriesMetadata{}, err
	}
	series, err := m.store.SeriesByIDAny(ctx, seriesID, false)
	if err != nil {
		return database.SeriesMetadata{}, err
	}
	if origin == "" {
		origin = "manual"
	}
	if origin != "manual" && origin != "agent" && origin != "mapping" && origin != "auto" {
		return database.SeriesMetadata{}, ErrInvalidConfig
	}
	if sourceID == TargetSourceBangumi {
		if m.bangumi == nil {
			return database.SeriesMetadata{}, ErrUnavailable
		}
		id, ok := bangumi.ParseSubjectIDInput(entryID)
		if !ok {
			return database.SeriesMetadata{}, ErrInvalidConfig
		}
		return m.bangumi.BindSubjectWithOrigin(ctx, series, id, origin)
	}
	src, err := m.store.GetMetadataSource(ctx, sourceID)
	if err != nil {
		return database.SeriesMetadata{}, err
	}
	if !src.Enabled || !src.Ready || src.Kind != KindOfflineSQLite {
		return database.SeriesMetadata{}, ErrUnavailable
	}
	entry, err := m.entry(ctx, sourceID, entryID)
	if err != nil {
		return database.SeriesMetadata{}, err
	}
	return m.store.ApplyScrapedMetadata(ctx, series, m.externalMetadata(ctx, src, entry, origin, 1))
}

func (m *Manager) sourceTagDictionary(ctx context.Context, sourceID string) *TagDictionary {
	if sourceID == "" {
		return nil
	}
	source, err := m.store.GetMetadataSource(ctx, sourceID)
	if err != nil || !source.Enabled || !source.Ready {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dictionaries[sourceID]
}

func (m *Manager) externalMetadata(ctx context.Context, src database.MetadataSource, entry *Entry, origin string, confidence float64) database.ScrapedMetadata {
	tags := append([]string{}, entry.Tags...)
	if dict := m.sourceTagDictionary(ctx, src.TagDictSourceID); dict != nil {
		tags = dict.TranslateList(tags, TagDisplayMode(src.TagDisplayMode))
	}
	authors := make([]database.Author, 0, len(entry.Authors))
	for _, name := range entry.Authors {
		authors = append(authors, database.Author{Name: name, Role: "writer"})
	}
	altTitles := make([]database.AlternateTitle, 0, 1)
	if entry.TitleAlt != "" && entry.TitleAlt != entry.Title {
		altTitles = append(altTitles, database.AlternateTitle{Title: entry.TitleAlt})
	}
	genres := []string{}
	if entry.Category != "" {
		genres = append(genres, entry.Category)
	}
	return database.ScrapedMetadata{
		ExtSourceID: src.ID, ExtEntryID: entry.EntryID, ExtCoverURL: entry.CoverURL,
		MatchOrigin: origin, Confidence: confidence, Title: entry.Title, Summary: entry.Summary,
		AlternateTitles: altTitles, Authors: authors, Tags: tags, Genres: genres, IsNSFW: entry.IsNSFW,
	}
}

func (m *Manager) ClearSeriesExternal(ctx context.Context, seriesID string) (database.SeriesMetadata, error) {
	if err := checkAdmin(ctx); err != nil {
		return database.SeriesMetadata{}, err
	}
	series, err := m.store.SeriesByIDAny(ctx, seriesID, false)
	if err != nil {
		return database.SeriesMetadata{}, err
	}
	return m.store.ClearSeriesExternalBinding(ctx, series)
}

type MappingExport struct {
	Count          int             `json:"count"`
	ExcludedNSFW   int             `json:"excludedNsfw"`
	SkippedPrivacy int             `json:"skippedPrivacy"`
	Sample         []MappingRecord `json:"sample"`
	NDJSON         []byte          `json:"-"`
}

func (m *Manager) ExportCommunityMappings(ctx context.Context, filter database.MappingExportFilter, preview bool) (MappingExport, error) {
	if err := checkAdmin(ctx); err != nil {
		return MappingExport{}, err
	}
	safeOnly := !filter.IncludeNSFW
	filter.IncludeNSFW = true // Collect the exclusion count from the same selection.
	rows, err := m.store.ListSeriesForMappingExport(ctx, filter)
	if err != nil {
		return MappingExport{}, err
	}
	plan, err := m.scrapePlan(ctx)
	if err != nil {
		return MappingExport{}, err
	}
	result := MappingExport{Sample: []MappingRecord{}}
	records := make([]MappingRecord, 0, len(rows))
	for _, row := range rows {
		if safeOnly && row.IsNSFW {
			result.ExcludedNSFW++
			continue
		}
		if strings.ContainsAny(row.FolderName, "/\\") || containsSensitiveOrPrivateValue(row.FolderName) || hasControlChars(row.FolderName) {
			result.SkippedPrivacy++
			continue
		}
		parsed := bangumi.ParseSeriesName(row.FolderName)
		key := firstNonEmpty(append(parsed.Candidates, row.FolderName)...)
		var author string
		if len(parsed.Authors) > 0 {
			author = parsed.Authors[0]
		} else if len(row.Authors) > 0 {
			author = row.Authors[0].Name
		}
		rec := MappingRecord{
			Schema: MappingSchemaV1, Key: key, RawKey: row.FolderName, Author: author,
			IsNSFW: row.IsNSFW, Origin: row.MatchOrigin,
		}
		switch {
		case row.BgmSubjectID != nil:
			rec.TargetSource, rec.TargetID = TargetSourceBangumi, strconv.FormatInt(*row.BgmSubjectID, 10)
		default:
			rec.TargetID = row.ExtEntryID
			m.mu.RLock()
			if sidecar := m.sidecars[row.ExtSourceID]; sidecar != nil {
				rec.TargetSource = sidecar.Schema()
			}
			m.mu.RUnlock()
			if rec.TargetSource == "" {
				rec.TargetSource = plan.sources[row.ExtSourceID].SchemaType
			}
		}
		if !isValidTargetSource(rec.TargetSource) {
			continue
		}
		if _, ok := sanitizeMappingRecord(rec, true); !ok {
			result.SkippedPrivacy++
			continue
		}
		records = append(records, rec)
	}
	data, err := SanitizeAndExportMappings(records, ExportMappingsOptions{IncludeNSFW: !safeOnly})
	if err != nil {
		return MappingExport{}, err
	}
	result.Count = bytes.Count(data, []byte{'\n'})
	sample := bytes.SplitAfterN(data, []byte{'\n'}, 31)
	result.Sample, err = ParseMappingsNDJSON(bytes.NewReader(bytes.Join(sample[:min(30, len(sample))], nil)))
	if result.Sample == nil {
		result.Sample = []MappingRecord{}
	}
	if !preview {
		result.NDJSON = data
	}
	return result, err
}
