package extmeta

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const maxEmbeddedMetaBytes int64 = 512 << 10 // 512 KiB

var (
	galleryPathIDRe = regexp.MustCompile(`(?i)/g/(\d+)/[0-9a-f]+`)
	bgmSubjectIDRe  = regexp.MustCompile(`(?i)/subject/(\d+)`)
)

// EmbeddedMeta holds metadata parsed directly from archive-embedded ComicInfo.xml
// or galleryinfo.txt sidecar files.
type EmbeddedMeta struct {
	Title       string   `json:"title,omitempty"`
	Series      string   `json:"series,omitempty"`
	Authors     []string `json:"authors,omitempty"`
	Summary     string   `json:"summary,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Genre       []string `json:"genre,omitempty"`
	ExternalURL string   `json:"externalUrl,omitempty"`
	ExternalID  string   `json:"externalId,omitempty"`
	IsNSFW      bool     `json:"isNsfw,omitempty"`
}

type comicInfoXMLDoc struct {
	XMLName     xml.Name `xml:"ComicInfo"`
	Title       string   `xml:"Title"`
	Series      string   `xml:"Series"`
	Summary     string   `xml:"Summary"`
	Writer      string   `xml:"Writer"`
	Penciller   string   `xml:"Penciller"`
	Inker       string   `xml:"Inker"`
	Colorist    string   `xml:"Colorist"`
	Letterer    string   `xml:"Letterer"`
	CoverArtist string   `xml:"CoverArtist"`
	Editor      string   `xml:"Editor"`
	Translator  string   `xml:"Translator"`
	Genre       string   `xml:"Genre"`
	Tags        string   `xml:"Tags"`
	Web         string   `xml:"Web"`
	AgeRating   string   `xml:"AgeRating"`
}

// ParseComicInfoXML parses a ComicInfo.xml stream up to 512 KiB using standard encoding/xml.
func ParseComicInfoXML(r io.Reader) (*EmbeddedMeta, error) {
	if r == nil {
		return nil, errors.New("ComicInfo.xml reader must not be nil")
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxEmbeddedMetaBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read ComicInfo.xml: %w", err)
	}
	if int64(len(raw)) > maxEmbeddedMetaBytes {
		return nil, fmt.Errorf("ComicInfo.xml exceeds %d bytes limit", maxEmbeddedMetaBytes)
	}
	raw = bytes.TrimPrefix(raw, []byte("\xEF\xBB\xBF"))
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("empty ComicInfo.xml")
	}

	var doc comicInfoXMLDoc
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = true
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode ComicInfo.xml: %w", err)
	}

	var rawAuthors []string
	for _, field := range []string{
		doc.Writer, doc.Penciller, doc.Inker, doc.Colorist,
		doc.Letterer, doc.CoverArtist, doc.Editor, doc.Translator,
	} {
		rawAuthors = append(rawAuthors, splitMetaList(field)...)
	}

	extURL := cleanSafeExternalURL(doc.Web)
	extID := extractExternalIDFromURL(extURL)

	return &EmbeddedMeta{
		Title:       strings.TrimSpace(doc.Title),
		Series:      strings.TrimSpace(doc.Series),
		Authors:     dedupeStrings(rawAuthors),
		Summary:     strings.TrimSpace(doc.Summary),
		Tags:        dedupeStrings(splitMetaList(doc.Tags)),
		Genre:       dedupeStrings(splitMetaList(doc.Genre)),
		ExternalURL: extURL,
		ExternalID:  extID,
		IsNSFW:      isComicInfoAgeRatingNSFW(doc.AgeRating),
	}, nil
}

// ParseGalleryInfoTXT parses a galleryinfo.txt metadata stream up to 512 KiB using bufio.
func ParseGalleryInfoTXT(r io.Reader) (*EmbeddedMeta, error) {
	if r == nil {
		return nil, errors.New("galleryinfo.txt reader must not be nil")
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxEmbeddedMetaBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read galleryinfo.txt: %w", err)
	}
	if int64(len(raw)) > maxEmbeddedMetaBytes {
		return nil, fmt.Errorf("galleryinfo.txt exceeds %d bytes limit", maxEmbeddedMetaBytes)
	}
	raw = bytes.TrimPrefix(raw, []byte("\xEF\xBB\xBF"))
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("empty galleryinfo.txt")
	}

	meta := &EmbeddedMeta{}
	var (
		rawTags       []string
		directAuthors []string
		category      string
		jpnTitle      string
		inComment     bool
		commentLines  []string
	)

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 16<<10), int(maxEmbeddedMetaBytes))

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if inComment {
			commentLines = append(commentLines, line)
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:colon]))
		val := strings.TrimSpace(line[colon+1:])

		switch key {
		case "title":
			if meta.Title == "" {
				meta.Title = val
			}
		case "japanese title", "title_jpn", "original title":
			if jpnTitle == "" {
				jpnTitle = val
			}
		case "series":
			if meta.Series == "" {
				meta.Series = val
			}
		case "artist", "author", "authors", "group", "circle":
			directAuthors = append(directAuthors, splitMetaList(val)...)
		case "tags", "tag":
			rawTags = append(rawTags, splitMetaList(val)...)
		case "category":
			category = val
		case "url", "source", "source url", "gallery url":
			if safeURL := cleanSafeExternalURL(val); safeURL != "" {
				meta.ExternalURL = safeURL
				if meta.ExternalID == "" {
					meta.ExternalID = extractExternalIDFromURL(safeURL)
				}
			}
		case "gid", "id", "gallery id":
			if meta.ExternalID == "" && val != "" {
				meta.ExternalID = val
			}
		case "uploader comment", "comment", "summary", "description":
			inComment = true
			if val != "" {
				commentLines = append(commentLines, val)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan galleryinfo.txt: %w", err)
	}

	if meta.Title == "" && jpnTitle != "" {
		meta.Title = jpnTitle
	}
	if meta.Series == "" && meta.Title != "" {
		meta.Series = meta.Title
	}

	parsedAuthors, parsedTags := ParseGalleryTags(rawTags)
	meta.Authors = dedupeStrings(append(directAuthors, parsedAuthors...))
	meta.Tags = parsedTags

	if category != "" {
		meta.Genre = []string{category}
		meta.IsNSFW = IsGalleryCategoryNSFW(category)
	}
	if len(commentLines) > 0 {
		meta.Summary = strings.TrimSpace(strings.Join(commentLines, "\n"))
	}

	return meta, nil
}

func splitMetaList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';'
	})
}

func isComicInfoAgeRatingNSFW(rating string) bool {
	switch strings.ToLower(strings.TrimSpace(rating)) {
	case "adults only 18+", "18+", "r18", "r18+", "x", "explicit":
		return true
	default:
		return false
	}
}

func cleanSafeExternalURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.String()
	default:
		return ""
	}
}

func extractExternalIDFromURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	if m := galleryPathIDRe.FindStringSubmatch(rawURL); len(m) == 2 {
		return m[1]
	}
	if m := bgmSubjectIDRe.FindStringSubmatch(rawURL); len(m) == 2 {
		return m[1]
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	trimmedPath := strings.Trim(u.Path, "/")
	if trimmedPath == "" {
		return ""
	}
	parts := strings.Split(trimmedPath, "/")
	last := parts[len(parts)-1]
	if _, err := strconv.ParseInt(last, 10, 64); err == nil {
		return last
	}
	return ""
}
