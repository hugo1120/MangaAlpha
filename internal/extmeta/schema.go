package extmeta

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	// SchemaAuto automatically inspects sqlite_master to detect the upstream schema.
	SchemaAuto = ""
	// SchemaMangaAlphaMetaV1 identifies the single-table MangaAlpha open metadata schema.
	SchemaMangaAlphaMetaV1 = "mangaalpha_meta_v1"
	// SchemaGalleryV1 identifies the normalized 4-table gallery metadata schema.
	SchemaGalleryV1 = "gallery_v1"
)

var (
	// ErrUnsupportedSchema is returned when an upstream SQLite database does not match a known schema.
	ErrUnsupportedSchema = errors.New("unsupported upstream metadata schema")
	// ErrEntryNotFound is returned when an entry ID does not exist in the upstream database.
	ErrEntryNotFound = errors.New("metadata entry not found")
)

// Entry is the unified metadata record returned from any supported offline SQLite schema.
type Entry struct {
	SourceID  string         `json:"sourceId,omitempty"`
	EntryID   string         `json:"entryId"`
	Token     string         `json:"token,omitempty"`
	Title     string         `json:"title"`
	TitleAlt  string         `json:"titleAlt,omitempty"`
	Authors   []string       `json:"authors,omitempty"`
	Category  string         `json:"category,omitempty"`
	Tags      []string       `json:"tags,omitempty"`
	Summary   string         `json:"summary,omitempty"`
	CoverURL  string         `json:"coverUrl,omitempty"`
	Rating    float64        `json:"rating,omitempty"`
	PostedAt  int64          `json:"postedAt,omitempty"`
	IsNSFW    bool           `json:"isNsfw"`
	SourceURL string         `json:"sourceUrl,omitempty"`
	RawFields map[string]any `json:"rawFields,omitempty"`
}

func openReadOnlySQLite(path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("sqlite path must not be empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(abs)
	uriPath = strings.ReplaceAll(uriPath, "?", "%3F")
	uriPath = strings.ReplaceAll(uriPath, "#", "%23")
	dsn := "file:" + uriPath + "?mode=ro&immutable=1&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// DetectSchema inspects tables in an already-open upstream SQLite database and returns
// either SchemaMangaAlphaMetaV1 or SchemaGalleryV1.
func DetectSchema(ctx context.Context, db *sql.DB, requested string) (string, error) {
	req := strings.ToLower(strings.TrimSpace(requested))
	if req == "auto" {
		req = ""
	}

	tables, err := listSQLiteTables(ctx, db)
	if err != nil {
		return "", err
	}

	switch req {
	case SchemaMangaAlphaMetaV1:
		if !tables["entries"] {
			return "", fmt.Errorf("%w: missing entries table for %s", ErrUnsupportedSchema, SchemaMangaAlphaMetaV1)
		}
		return SchemaMangaAlphaMetaV1, nil
	case SchemaGalleryV1:
		if !tables["gallery"] {
			return "", fmt.Errorf("%w: missing gallery table for %s", ErrUnsupportedSchema, SchemaGalleryV1)
		}
		return SchemaGalleryV1, nil
	case "":
		if tables["entries"] {
			return SchemaMangaAlphaMetaV1, nil
		}
		if tables["gallery"] {
			return SchemaGalleryV1, nil
		}
		return "", fmt.Errorf("%w: no recognized entries or gallery table found", ErrUnsupportedSchema)
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedSchema, requested)
	}
}

func listSQLiteTables(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type IN ('table','view')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			tables[strings.ToLower(strings.TrimSpace(name))] = true
		}
	}
	return tables, rows.Err()
}

// IsGalleryCategoryNSFW applies the gallery_v1 NSFW rule: only category "Non-H"
// (case-insensitive, trimmed) is non-NSFW; all other categories default to true.
func IsGalleryCategoryNSFW(category string) bool {
	return !strings.EqualFold(strings.TrimSpace(category), "Non-H")
}

// ParseGalleryTags splits raw gallery_v1 tags into extracted Authors (from artist:/group:)
// and remaining namespace:tag entries, deduplicated in order.
func ParseGalleryTags(rawTags []string) (authors []string, tags []string) {
	seenAuthors := make(map[string]struct{})
	seenTags := make(map[string]struct{})

	for _, raw := range rawTags {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		lower := strings.ToLower(t)
		if strings.HasPrefix(lower, "artist:") {
			name := strings.TrimSpace(t[len("artist:"):])
			if name != "" {
				key := strings.ToLower(name)
				if _, dup := seenAuthors[key]; !dup {
					seenAuthors[key] = struct{}{}
					authors = append(authors, name)
				}
			}
			continue
		}
		if strings.HasPrefix(lower, "group:") {
			name := strings.TrimSpace(t[len("group:"):])
			if name != "" {
				key := strings.ToLower(name)
				if _, dup := seenAuthors[key]; !dup {
					seenAuthors[key] = struct{}{}
					authors = append(authors, name)
				}
			}
			continue
		}
		if _, dup := seenTags[lower]; !dup {
			seenTags[lower] = struct{}{}
			tags = append(tags, t)
		}
	}
	return authors, tags
}

func getEntryFromUpstream(ctx context.Context, db *sql.DB, sourceID, schema, entryID string) (*Entry, error) {
	entryID = strings.TrimSpace(entryID)
	if entryID == "" {
		return nil, ErrEntryNotFound
	}
	switch schema {
	case SchemaMangaAlphaMetaV1:
		return getMangaAlphaMetaV1Entry(ctx, db, sourceID, entryID)
	case SchemaGalleryV1:
		return getGalleryV1Entry(ctx, db, sourceID, entryID)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedSchema, schema)
	}
}

func getMangaAlphaMetaV1Entry(ctx context.Context, db *sql.DB, sourceID, entryID string) (*Entry, error) {
	var (
		id, title, titleAlt, authorsRaw, summary, tagsRaw, category, coverURL, extraJSON sql.NullString
		rating                                                                           sql.NullFloat64
		isNSFW, updatedAt                                                                sql.NullInt64
	)
	err := db.QueryRowContext(ctx, `
SELECT id, title, title_alt, authors, summary, tags, category, cover_url, rating, is_nsfw, updated_at, extra_json
FROM entries WHERE id = ?`, entryID).Scan(
		&id, &title, &titleAlt, &authorsRaw, &summary, &tagsRaw,
		&category, &coverURL, &rating, &isNSFW, &updatedAt, &extraJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEntryNotFound
	}
	if err != nil {
		return nil, err
	}

	entry := &Entry{
		SourceID: sourceID,
		EntryID:  strings.TrimSpace(id.String),
		Title:    strings.TrimSpace(title.String),
		TitleAlt: strings.TrimSpace(titleAlt.String),
		Authors:  parseStringListField(authorsRaw.String),
		Category: strings.TrimSpace(category.String),
		Tags:     parseStringListField(tagsRaw.String),
		Summary:  strings.TrimSpace(summary.String),
		CoverURL: strings.TrimSpace(coverURL.String),
		Rating:   rating.Float64,
		PostedAt: updatedAt.Int64,
		IsNSFW:   isNSFW.Int64 != 0,
	}

	if raw := strings.TrimSpace(extraJSON.String); raw != "" {
		var fields map[string]any
		if json.Unmarshal([]byte(raw), &fields) == nil && len(fields) > 0 {
			entry.RawFields = fields
			if entry.Token == "" {
				if v, ok := fields["token"].(string); ok {
					entry.Token = strings.TrimSpace(v)
				}
			}
			if entry.SourceURL == "" {
				if v, ok := fields["source_url"].(string); ok {
					entry.SourceURL = strings.TrimSpace(v)
				} else if v, ok := fields["sourceUrl"].(string); ok {
					entry.SourceURL = strings.TrimSpace(v)
				}
			}
		}
	}
	return entry, nil
}

func getGalleryV1Entry(ctx context.Context, db *sql.DB, sourceID, entryID string) (*Entry, error) {
	gid, err := strconv.ParseInt(entryID, 10, 64)
	if err != nil {
		return nil, ErrEntryNotFound
	}

	var (
		token, title, titleJpn, category, thumb, uploader sql.NullString
		posted, filecount, filesize, rootGID              sql.NullInt64
		expunged, removed, replaced                       sql.NullInt64
		rating                                            sql.NullFloat64
	)
	err = db.QueryRowContext(ctx, `
SELECT token, title, title_jpn, category, thumb, uploader, posted,
       filecount, filesize, expunged, removed, replaced, rating, root_gid
FROM gallery WHERE gid = ?`, gid).Scan(
		&token, &title, &titleJpn, &category, &thumb, &uploader, &posted,
		&filecount, &filesize, &expunged, &removed, &replaced, &rating, &rootGID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrEntryNotFound
	}
	if err != nil {
		return nil, err
	}

	var rawTags []string
	if tables, tErr := listSQLiteTables(ctx, db); tErr == nil && tables["gid_tid"] && tables["tag"] {
		tRows, qErr := db.QueryContext(ctx, `
SELECT t.name
FROM gid_tid gt
JOIN tag t ON t.id = gt.tid
WHERE gt.gid = ?
ORDER BY gt.tid ASC`, gid)
		if qErr != nil {
			return nil, qErr
		}
		for tRows.Next() {
			var tagName sql.NullString
			if sErr := tRows.Scan(&tagName); sErr == nil && strings.TrimSpace(tagName.String) != "" {
				rawTags = append(rawTags, tagName.String)
			}
		}
		if rErr := tRows.Err(); rErr != nil {
			tRows.Close()
			return nil, rErr
		}
		tRows.Close()
	}

	authors, tags := ParseGalleryTags(rawTags)
	catStr := strings.TrimSpace(category.String)

	rawFields := map[string]any{
		"gid": gid,
	}
	if u := strings.TrimSpace(uploader.String); u != "" {
		rawFields["uploader"] = u
	}
	if filecount.Valid {
		rawFields["filecount"] = filecount.Int64
	}
	if filesize.Valid {
		rawFields["filesize"] = filesize.Int64
	}
	if expunged.Valid {
		rawFields["expunged"] = expunged.Int64 != 0
	}
	if removed.Valid {
		rawFields["removed"] = removed.Int64 != 0
	}
	if replaced.Valid {
		rawFields["replaced"] = replaced.Int64 != 0
	}
	if rootGID.Valid && rootGID.Int64 > 0 {
		rawFields["root_gid"] = rootGID.Int64
	}

	return &Entry{
		SourceID:  sourceID,
		EntryID:   strconv.FormatInt(gid, 10),
		Token:     strings.TrimSpace(token.String),
		Title:     strings.TrimSpace(title.String),
		TitleAlt:  strings.TrimSpace(titleJpn.String),
		Authors:   authors,
		Category:  catStr,
		Tags:      tags,
		CoverURL:  strings.TrimSpace(thumb.String),
		Rating:    rating.Float64,
		PostedAt:  posted.Int64,
		IsNSFW:    IsGalleryCategoryNSFW(catStr),
		RawFields: rawFields,
	}, nil
}

func parseStringListField(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var items []string
		if json.Unmarshal([]byte(raw), &items) == nil {
			return dedupeStrings(items)
		}
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '|' || r == '\n'
	})
	return dedupeStrings(parts)
}

func dedupeStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, it := range items {
		trimmed := strings.TrimSpace(it)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
