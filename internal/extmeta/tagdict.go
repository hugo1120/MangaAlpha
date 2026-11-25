package extmeta

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxTagDictBytes int64 = 64 << 20 // 64 MiB uncompressed limit

// TagDisplayMode controls how translated tags are formatted.
type TagDisplayMode string

const (
	// ModeZH outputs only the Chinese translation, falling back to the raw tag when missing.
	ModeZH TagDisplayMode = "zh"
	// ModeBilingual outputs "中文 (raw)" when a translation exists, or the raw tag otherwise.
	ModeBilingual TagDisplayMode = "bilingual"
	// ModeRaw always outputs the trimmed raw tag unmodified.
	ModeRaw TagDisplayMode = "raw"
)

type rawTagDictionaryFile struct {
	Data []struct {
		Namespace string `json:"namespace"`
		Data      map[string]struct {
			Name string `json:"name"`
		} `json:"data"`
	} `json:"data"`
}

// TagDictionary provides concurrency-safe read-only Chinese tag translation lookups
// for tag_translation_text_v1 dictionaries (db.text.json / db.text.json.gz).
type TagDictionary struct {
	byNS  map[string]map[string]string
	byRaw map[string]string
	count int
}

// ParseTagDictionary reads a tag_translation_text_v1 JSON or gzip-compressed JSON stream
// and constructs an immutable, thread-safe TagDictionary.
func ParseTagDictionary(r io.Reader) (*TagDictionary, error) {
	if r == nil {
		return nil, errors.New("tag dictionary reader must not be nil")
	}
	br := bufio.NewReaderSize(r, 64<<10)
	magic, err := br.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("peek tag dictionary stream: %w", err)
	}

	var src io.Reader = br
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, gErr := gzip.NewReader(br)
		if gErr != nil {
			return nil, fmt.Errorf("open gzip tag dictionary: %w", gErr)
		}
		defer gz.Close()
		src = gz
	}

	payload, err := io.ReadAll(io.LimitReader(src, maxTagDictBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read tag dictionary: %w", err)
	}
	if int64(len(payload)) > maxTagDictBytes {
		return nil, fmt.Errorf("tag dictionary exceeds %d bytes limit", maxTagDictBytes)
	}
	if len(payload) == 0 {
		return nil, errors.New("empty tag dictionary payload")
	}

	var raw rawTagDictionaryFile
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("decode tag dictionary JSON: %w", err)
	}

	byNS := make(map[string]map[string]string, len(raw.Data))
	byRaw := make(map[string]string)
	count := 0

	for _, nsBlock := range raw.Data {
		ns := strings.ToLower(strings.TrimSpace(nsBlock.Namespace))
		if len(nsBlock.Data) == 0 {
			continue
		}
		nsMap, ok := byNS[ns]
		if !ok {
			nsMap = make(map[string]string, len(nsBlock.Data))
			byNS[ns] = nsMap
		}
		for rawTag, entry := range nsBlock.Data {
			key := strings.ToLower(strings.TrimSpace(rawTag))
			zh := strings.TrimSpace(entry.Name)
			if key == "" || zh == "" {
				continue
			}
			if _, exists := nsMap[key]; !exists {
				count++
			}
			nsMap[key] = zh
			if ns != "rows" {
				if _, exists := byRaw[key]; !exists {
					byRaw[key] = zh
				}
			}
		}
	}

	return &TagDictionary{
		byNS:  byNS,
		byRaw: byRaw,
		count: count,
	}, nil
}

// LoadTagDictionaryFile opens and parses a local db.text.json or db.text.json.gz file.
func LoadTagDictionaryFile(path string) (*TagDictionary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseTagDictionary(f)
}

// Len returns the number of translated tag entries loaded in the dictionary.
func (d *TagDictionary) Len() int {
	if d == nil {
		return 0
	}
	return d.count
}

// Lookup looks up the Chinese translation of a tag (supports both "namespace:tag" and "tag").
func (d *TagDictionary) Lookup(rawTag string) (string, bool) {
	if d == nil {
		return "", false
	}
	trimmed := strings.TrimSpace(rawTag)
	if trimmed == "" {
		return "", false
	}
	lower := strings.ToLower(trimmed)
	if idx := strings.IndexByte(lower, ':'); idx > 0 {
		ns := strings.TrimSpace(lower[:idx])
		name := strings.TrimSpace(lower[idx+1:])
		if nsMap, ok := d.byNS[ns]; ok {
			if zh, found := nsMap[name]; found {
				return zh, true
			}
		}
		return "", false
	}
	if zh, ok := d.byRaw[lower]; ok {
		return zh, true
	}
	return "", false
}

// Translate formats a single tag according to mode (ModeZH, ModeBilingual, ModeRaw).
func (d *TagDictionary) Translate(rawTag string, mode TagDisplayMode) string {
	trimmed := strings.TrimSpace(rawTag)
	if trimmed == "" {
		return ""
	}
	switch mode {
	case ModeRaw:
		return trimmed
	case ModeZH:
		if zh, ok := d.Lookup(trimmed); ok && zh != "" {
			return zh
		}
		return trimmed
	case ModeBilingual:
		if zh, ok := d.Lookup(trimmed); ok && zh != "" && zh != trimmed {
			return fmt.Sprintf("%s (%s)", zh, trimmed)
		}
		return trimmed
	default:
		return trimmed
	}
}

// TranslateList translates a list of tags using mode and returns a deduplicated slice in order.
func (d *TagDictionary) TranslateList(tags []string, mode TagDisplayMode) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, raw := range tags {
		translated := d.Translate(raw, mode)
		if translated == "" {
			continue
		}
		key := strings.ToLower(translated)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, translated)
	}
	return out
}
