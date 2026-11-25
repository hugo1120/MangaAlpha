package extmeta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	// RecommendedRepoURL is the suggested default community repository URL for admin UI pre-fill.
	RecommendedRepoURL = "https://raw.githubusercontent.com/BlackMuseCode/mangaalpha-metadata-repo/main/repo.json"

	// RepoSchemaV1 is the required schema identifier for repository manifests.
	RepoSchemaV1 = "mangaalpha-repo-v1"

	// MaxRepoManifestBytes bounds manifest downloads and parsing to 4 MiB.
	MaxRepoManifestBytes = 4 << 20

	// Supported repository source kinds.
	KindBangumiArchive = "bangumi_archive"
	KindOfflineSQLite  = "offline_sqlite"
	KindTagDictionary  = "tag_dictionary"
	KindMappingPack    = "mapping_pack"
)

var (
	// ErrInvalidManifest identifies invalid repository manifest content.
	ErrInvalidManifest = errors.New("invalid metadata repository manifest")

	validSourceIDRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	repoUserAgent   = "MangaAlpha/3.2"
)

// HTTPStatusError reports an unexpected upstream HTTP status for a metadata operation.
type HTTPStatusError struct {
	StatusCode int
	Op         string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("%s returned HTTP %d", e.Op, e.StatusCode)
}

// RepoManifest describes a Mihon-style declarative external metadata repository.
type RepoManifest struct {
	Schema      string       `json:"schema"`
	Name        string       `json:"name,omitempty"`
	Description string       `json:"description,omitempty"`
	Homepage    string       `json:"homepage,omitempty"`
	Version     string       `json:"version,omitempty"`
	UpdatedAt   string       `json:"updatedAt,omitempty"`
	Sources     []RepoSource `json:"sources"`
}

// AutoUpdatePreference is the default update schedule declared by a source.
type AutoUpdatePreference string

func (p *AutoUpdatePreference) UnmarshalJSON(data []byte) error {
	switch strings.TrimSpace(string(data)) {
	case "true":
		*p = "weekly"
	case "false":
		*p = "manual"
	default:
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		switch value = strings.ToLower(strings.TrimSpace(value)); value {
		case "manual", "weekly", "monthly":
			*p = AutoUpdatePreference(value)
		default:
			return errors.New("autoUpdateDefault must be manual, weekly, or monthly")
		}
	}
	return nil
}

// RepoSource declares a single external metadata source, tag dictionary, or mapping pack.
type RepoSource struct {
	ID                string               `json:"id"`
	Kind              string               `json:"kind"`
	Schema            string               `json:"schema,omitempty"`
	Name              string               `json:"name"`
	Description       string               `json:"description,omitempty"`
	ManifestURL       string               `json:"manifestUrl,omitempty"`
	DownloadURL       string               `json:"downloadUrl,omitempty"`
	ReleaseAPIURL     string               `json:"releaseApiUrl,omitempty"`
	AssetName         string               `json:"assetName,omitempty"`
	Compression       string               `json:"compression,omitempty"`
	SizeHintBytes     int64                `json:"sizeHintBytes,omitempty"`
	DefaultTagDictID  string               `json:"defaultTagDictId,omitempty"`
	NSFWRule          string               `json:"nsfwRule,omitempty"`
	AutoUpdateDefault AutoUpdatePreference `json:"autoUpdateDefault,omitempty"`
	Homepage          string               `json:"homepage,omitempty"`
	ContributeURL     string               `json:"contributeUrl,omitempty"`
	Default           bool                 `json:"default,omitempty"`
}

// ParseRepoManifest parses and validates a mangaalpha-repo-v1 JSON payload.
func ParseRepoManifest(data []byte) (*RepoManifest, error) {
	manifest, err := parseRepoManifest(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidManifest, err)
	}
	return manifest, nil
}

func parseRepoManifest(data []byte) (*RepoManifest, error) {
	if len(data) == 0 {
		return nil, errors.New("empty repo manifest")
	}
	if len(data) > MaxRepoManifestBytes {
		return nil, fmt.Errorf("repo manifest exceeds %d bytes limit", MaxRepoManifestBytes)
	}

	var manifest RepoManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("decode repo manifest: %w", err)
	}
	manifest.Schema = strings.TrimSpace(manifest.Schema)
	if manifest.Schema != RepoSchemaV1 {
		return nil, fmt.Errorf("unsupported repo manifest schema %q (want %q)", manifest.Schema, RepoSchemaV1)
	}
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Description = strings.TrimSpace(manifest.Description)
	manifest.Homepage = strings.TrimSpace(manifest.Homepage)
	manifest.Version = strings.TrimSpace(manifest.Version)
	manifest.UpdatedAt = strings.TrimSpace(manifest.UpdatedAt)
	if manifest.Homepage != "" {
		if _, err := validateHTTPURL(manifest.Homepage); err != nil {
			return nil, fmt.Errorf("invalid manifest homepage URL: %w", err)
		}
	}

	seenIDs := make(map[string]struct{}, len(manifest.Sources))
	for i := range manifest.Sources {
		src := &manifest.Sources[i]
		src.ID = strings.TrimSpace(src.ID)
		src.Kind = strings.TrimSpace(src.Kind)
		src.Schema = strings.TrimSpace(src.Schema)
		src.Name = strings.TrimSpace(src.Name)
		src.Description = strings.TrimSpace(src.Description)
		src.ManifestURL = strings.TrimSpace(src.ManifestURL)
		src.DownloadURL = strings.TrimSpace(src.DownloadURL)
		src.ReleaseAPIURL = strings.TrimSpace(src.ReleaseAPIURL)
		src.AssetName = strings.TrimSpace(src.AssetName)
		src.Compression = strings.ToLower(strings.TrimSpace(src.Compression))
		src.DefaultTagDictID = strings.TrimSpace(src.DefaultTagDictID)
		src.NSFWRule = strings.TrimSpace(src.NSFWRule)
		src.Homepage = strings.TrimSpace(src.Homepage)
		src.ContributeURL = strings.TrimSpace(src.ContributeURL)

		if !isValidSourceID(src.ID) {
			return nil, fmt.Errorf("source[%d] has invalid id %q", i, src.ID)
		}
		if _, dup := seenIDs[src.ID]; dup {
			return nil, fmt.Errorf("duplicate source id %q", src.ID)
		}
		seenIDs[src.ID] = struct{}{}

		if !isValidSourceKind(src.Kind) {
			return nil, fmt.Errorf("source %q has unsupported kind %q", src.ID, src.Kind)
		}
		if src.Name == "" {
			return nil, fmt.Errorf("source %q missing name", src.ID)
		}
		if src.SizeHintBytes < 0 {
			return nil, fmt.Errorf("source %q has negative sizeHintBytes", src.ID)
		}

		for field, rawURL := range map[string]string{
			"manifestUrl":   src.ManifestURL,
			"downloadUrl":   src.DownloadURL,
			"releaseApiUrl": src.ReleaseAPIURL,
			"homepage":      src.Homepage,
			"contributeUrl": src.ContributeURL,
		} {
			if rawURL == "" || field == "manifestUrl" && src.Kind == KindBangumiArchive && (rawURL == "zh_cn" || rawURL == "official") {
				continue
			}
			if u, err := validateHTTPURL(rawURL); err != nil || u.User != nil {
				return nil, fmt.Errorf("source %q invalid %s: %w", src.ID, field, ErrInvalidURL)
			}
		}
	}

	return &manifest, nil
}

// FetchRepoManifest fetches, size-bounds (<= 4 MiB), and validates a remote repo manifest.
func FetchRepoManifest(ctx context.Context, client *http.Client, rawURL string) (*RepoManifest, error) {
	if _, err := validateHTTPURL(rawURL); err != nil {
		return nil, fmt.Errorf("invalid repo manifest URL: %w", err)
	}
	if client == nil {
		client = NewSafeHTTPClient("", 30*time.Second)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(rawURL), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", repoUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPStatusError{StatusCode: resp.StatusCode, Op: "fetch repo manifest"}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxRepoManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read repo manifest: %w", err)
	}
	return ParseRepoManifest(body)
}

func isValidSourceID(id string) bool {
	if id == "" || len(id) > 128 || id == "." || id == ".." {
		return false
	}
	return validSourceIDRe.MatchString(id)
}

func isValidSourceKind(kind string) bool {
	switch kind {
	case KindBangumiArchive, KindOfflineSQLite, KindTagDictionary, KindMappingPack:
		return true
	default:
		return false
	}
}
