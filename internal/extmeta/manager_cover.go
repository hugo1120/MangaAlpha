package extmeta

import (
	"context"
	"strings"

	"github.com/xJogger/fake-komga-115/internal/database"
)

func (m *Manager) FetchCover(ctx context.Context, sourceID, entryID, coverURL string) ([]byte, string, error) {
	src, err := m.store.GetMetadataSource(ctx, sourceID)
	if err != nil {
		return nil, "", err
	}
	if !src.Enabled || src.Kind != KindOfflineSQLite || m.covers == nil || coverURL == "" {
		return nil, "", ErrUnavailable
	}
	if scope, ok := database.UserScopeFromContext(ctx); ok && !scope.AllowNSFW {
		entry, err := m.entry(ctx, sourceID, entryID)
		if err != nil || entry.IsNSFW {
			return nil, "", ErrUnavailable
		}
	}
	u, err := validateHTTPURL(coverURL)
	if err != nil || u.User != nil {
		return nil, "", ErrInvalidConfig
	}
	data, err := m.covers.GetOrFetch(ctx, sourceID, entryID, coverURL)
	return data.Bytes, data.MediaType, err
}

func (m *Manager) EntryCover(ctx context.Context, sourceID, entryID string) ([]byte, string, error) {
	if err := checkAdmin(ctx); err != nil {
		return nil, "", err
	}
	entry, err := m.entry(ctx, sourceID, strings.TrimSpace(entryID))
	if err != nil {
		return nil, "", err
	}
	return m.FetchCover(ctx, sourceID, entry.EntryID, entry.CoverURL)
}
