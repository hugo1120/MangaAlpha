package bangumi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/xJogger/fake-komga-115/internal/database"
	"github.com/xJogger/fake-komga-115/internal/diagnostics"
)

const (
	SettingTrackerEnabled        = "bangumi_tracker_enabled"
	SettingTrackerToken          = "bangumi_tracker_token"
	SettingTrackerNSFWMode       = "bangumi_tracker_nsfw_mode"
	SettingTrackerPrivateDefault = "bangumi_tracker_private_default"
	SettingTrackerUsername       = "bangumi_tracker_username"
	SettingTrackerNickname       = "bangumi_tracker_nickname"
	SettingTrackerUserID         = "bangumi_tracker_user_id"

	TrackerNSFWModeSkip    = "skip"
	TrackerNSFWModePrivate = "private"

	CollectionTypeWish    = 1 // 想读
	CollectionTypeCollect = 2 // 读过
	CollectionTypeDoing   = 3 // 在读
	CollectionTypeOnHold  = 4 // 搁置
	CollectionTypeDropped = 5 // 抛弃

	defaultTrackerDebounce = 5 * time.Second
)

var (
	ErrTrackerTokenInvalid = errors.New("Bangumi 授权已失效或过期，请重新点击 OAuth 授权或更换 Personal Access Token")
	ErrTrackerNotBound     = errors.New("请先通过 Bangumi OAuth 授权或配置有效的 Access Token")
	ErrTrackerUnboundItem  = errors.New("该作品尚未绑定 Bangumi 条目")
)

type TrackerConfigUpdate struct {
	Enabled                 *bool   `json:"enabled"`
	AccessToken             *string `json:"accessToken"`
	ClearToken              bool    `json:"clearToken"`
	NSFWMode                *string `json:"nsfwMode"`
	PrivateDefault          *bool   `json:"privateDefault"`
	CustomOAuthClientID     *string `json:"customOAuthClientId"`
	CustomOAuthClientSecret *string `json:"customOAuthClientSecret"`
	ClearCustomOAuthApp     bool    `json:"clearCustomOAuthApp"`
}

type TrackerUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
}

type TrackerStatus struct {
	Enabled              bool                            `json:"enabled"`
	HasToken             bool                            `json:"hasToken"`
	AuthMode             string                          `json:"authMode"`
	AutoRefresh          bool                            `json:"autoRefresh"`
	ExpiresAt            string                          `json:"expiresAt,omitempty"`
	ExpiresSoon          bool                            `json:"expiresSoon"`
	Expired              bool                            `json:"expired"`
	LastRefreshAt        string                          `json:"lastRefreshAt,omitempty"`
	LastRefreshError     string                          `json:"lastRefreshError,omitempty"`
	UsingCustomOAuthApp  bool                            `json:"usingCustomOAuthApp"`
	CustomOAuthClientID  string                          `json:"customOAuthClientId,omitempty"`
	HasCustomOAuthSecret bool                            `json:"hasCustomOAuthSecret"`
	Username             string                          `json:"username"`
	Nickname             string                          `json:"nickname"`
	NSFWMode             string                          `json:"nsfwMode"`
	PrivateDefault       bool                            `json:"privateDefault"`
	Records              []database.BangumiTrackerRecord `json:"records"`
}

type TrackerTestResult struct {
	OK        bool   `json:"ok"`
	Username  string `json:"username,omitempty"`
	Nickname  string `json:"nickname,omitempty"`
	ExpiresAt string `json:"expiresAt,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

type SeriesTrackerSummary struct {
	Enabled         bool    `json:"enabled"`
	Status          string  `json:"status"`
	RemoteType      int     `json:"remoteType"`
	RemoteVolStatus int     `json:"remoteVolStatus"`
	RemotePrivate   bool    `json:"remotePrivate"`
	LastError       string  `json:"lastError,omitempty"`
	LastSyncedAt    *string `json:"lastSyncedAt,omitempty"`
}

type trackerConfig struct {
	Enabled                 bool
	Token                   string
	AuthMode                string
	RefreshToken            string
	ExpiresAt               string
	LastRefreshAt           string
	LastRefreshError        string
	OAuthRedirectURI        string
	CustomOAuthClientID     string
	CustomOAuthClientSecret string
	Username                string
	Nickname                string
	UserID                  string
	NSFWMode                string
	PrivateDefault          bool
}

func (c trackerConfig) userPathSegment() string {
	if u := strings.TrimSpace(c.Username); u != "" {
		return u
	}
	return strings.TrimSpace(c.UserID)
}

type trackerQueueItem struct {
	UserID   string
	SeriesID string
}

type trackerManager struct {
	svc     *Service
	limiter *rate.Limiter

	mu           sync.Mutex
	refreshMu    sync.Mutex
	oauthBaseURL string
	debounce     time.Duration
	timers       map[string]*time.Timer
	queue        chan trackerQueueItem
	done         chan struct{}
	refreshDone  chan struct{}
	closed       bool
}

func newTrackerManager(svc *Service) *trackerManager {
	tm := &trackerManager{
		svc:         svc,
		limiter:     rate.NewLimiter(rate.Limit(1.0), 2),
		debounce:    defaultTrackerDebounce,
		timers:      make(map[string]*time.Timer),
		queue:       make(chan trackerQueueItem, 256),
		done:        make(chan struct{}),
		refreshDone: make(chan struct{}),
	}
	go tm.worker()
	go tm.refreshLoop()
	return tm
}

func (tm *trackerManager) Close() {
	if tm == nil {
		return
	}
	tm.mu.Lock()
	if tm.closed {
		tm.mu.Unlock()
		return
	}
	tm.closed = true
	for k, timer := range tm.timers {
		timer.Stop()
		delete(tm.timers, k)
	}
	tm.mu.Unlock()
	if tm.done != nil {
		<-tm.done
	}
	if tm.refreshDone != nil {
		<-tm.refreshDone
	}
}

func (tm *trackerManager) SetDebounce(d time.Duration) {
	if tm == nil || d <= 0 {
		return
	}
	tm.mu.Lock()
	tm.debounce = d
	tm.mu.Unlock()
}

func (tm *trackerManager) NotifyProgress(seriesID string) {
	tm.NotifyProgressForUser(database.PrimaryAdminUserID, seriesID)
}

func (tm *trackerManager) NotifyProgressForUser(userID, seriesID string) {
	seriesID = strings.TrimSpace(seriesID)
	userID = strings.TrimSpace(userID)
	if userID == "" {
		userID = database.PrimaryAdminUserID
	}
	if tm == nil || seriesID == "" {
		return
	}
	key := userID + "\x00" + seriesID
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.closed {
		return
	}
	if prev, exists := tm.timers[key]; exists {
		prev.Reset(tm.debounce)
		return
	}
	delay := tm.debounce
	tm.timers[key] = time.AfterFunc(delay, func() {
		tm.mu.Lock()
		delete(tm.timers, key)
		closed := tm.closed
		tm.mu.Unlock()
		if closed {
			return
		}
		select {
		case tm.queue <- trackerQueueItem{UserID: userID, SeriesID: seriesID}:
		case <-tm.svc.ctx.Done():
		default:
		}
	})
}

func (tm *trackerManager) worker() {
	defer close(tm.done)
	for {
		select {
		case <-tm.svc.ctx.Done():
			return
		case item := <-tm.queue:
			syncCtx, cancel := context.WithTimeout(database.WithUserID(tm.svc.ctx, item.UserID), 25*time.Second)
			_, _ = tm.SyncSeries(syncCtx, item.SeriesID, false)
			cancel()
		}
	}
}

func normalizeTrackerNSFWMode(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", TrackerNSFWModeSkip:
		return TrackerNSFWModeSkip, nil
	case TrackerNSFWModePrivate:
		return TrackerNSFWModePrivate, nil
	default:
		return "", fmt.Errorf("invalid nsfwMode %q (use skip or private)", raw)
	}
}

func (tm *trackerManager) loadConfig(ctx context.Context) trackerConfig {
	s := tm.svc.store
	if s == nil {
		return trackerConfig{NSFWMode: TrackerNSFWModeSkip}
	}
	userID := database.UserIDFromContext(ctx)
	row, _ := s.UserBangumiTrackerConfig(ctx, userID)
	customClientID, _ := s.GetSetting(ctx, SettingTrackerOAuthClientID)
	customClientSecret, _ := s.GetSetting(ctx, SettingTrackerOAuthClientSecret)

	nsfwMode, err := normalizeTrackerNSFWMode(row.NSFWMode)
	if err != nil {
		nsfwMode = TrackerNSFWModeSkip
	}
	token := strings.TrimSpace(row.AccessToken)
	authMode := strings.ToLower(strings.TrimSpace(row.AuthMode))
	if token != "" && authMode == "" {
		authMode = TrackerAuthModePAT
	}
	return trackerConfig{
		Enabled:                 row.Enabled,
		Token:                   token,
		AuthMode:                authMode,
		RefreshToken:            strings.TrimSpace(row.RefreshToken),
		ExpiresAt:               strings.TrimSpace(row.ExpiresAt),
		LastRefreshAt:           strings.TrimSpace(row.LastRefreshAt),
		LastRefreshError:        strings.TrimSpace(row.LastRefreshError),
		OAuthRedirectURI:        strings.TrimSpace(row.OAuthRedirectURI),
		CustomOAuthClientID:     strings.TrimSpace(customClientID),
		CustomOAuthClientSecret: strings.TrimSpace(customClientSecret),
		Username:                strings.TrimSpace(row.BgmUsername),
		Nickname:                strings.TrimSpace(row.BgmNickname),
		UserID:                  strings.TrimSpace(row.BgmUserID),
		NSFWMode:                nsfwMode,
		PrivateDefault:          row.PrivateDefault,
	}
}

func (tm *trackerManager) saveUserConfig(ctx context.Context, cfg trackerConfig) error {
	if tm.svc.store == nil {
		return nil
	}
	return tm.svc.store.SaveUserBangumiTrackerConfig(ctx, database.UserBangumiTracker{
		UserID:           database.UserIDFromContext(ctx),
		Enabled:          cfg.Enabled,
		AuthMode:         cfg.AuthMode,
		AccessToken:      cfg.Token,
		RefreshToken:     cfg.RefreshToken,
		ExpiresAt:        cfg.ExpiresAt,
		LastRefreshAt:    cfg.LastRefreshAt,
		LastRefreshError: cfg.LastRefreshError,
		OAuthRedirectURI: cfg.OAuthRedirectURI,
		BgmUserID:        cfg.UserID,
		BgmUsername:      cfg.Username,
		BgmNickname:      cfg.Nickname,
		NSFWMode:         cfg.NSFWMode,
		PrivateDefault:   cfg.PrivateDefault,
	})
}

func (tm *trackerManager) Status(ctx context.Context, recordLimit int) (TrackerStatus, error) {
	cfg := tm.loadConfig(ctx)
	records, err := tm.svc.store.ListBangumiTrackerRecords(ctx, recordLimit)
	if err != nil {
		return TrackerStatus{}, err
	}
	now := tm.svc.now().UTC()
	expired := false
	expiresSoon := false
	if cfg.Token != "" && cfg.ExpiresAt != "" {
		if exp, pErr := time.Parse(time.RFC3339, cfg.ExpiresAt); pErr == nil {
			rem := exp.Sub(now)
			if rem <= 0 {
				expired = true
			} else if cfg.AuthMode == TrackerAuthModePAT && rem <= patWarnSoonWindow {
				expiresSoon = true
			} else if cfg.AuthMode == TrackerAuthModeOAuth && rem <= oauthSyncRefreshWindow {
				expiresSoon = true
			}
		}
	}
	_, _, usingCustom := tm.effectiveOAuthApp(cfg)
	return TrackerStatus{
		Enabled:              cfg.Enabled && cfg.Token != "",
		HasToken:             cfg.Token != "",
		AuthMode:             cfg.AuthMode,
		AutoRefresh:          cfg.AuthMode == TrackerAuthModeOAuth && cfg.RefreshToken != "",
		ExpiresAt:            cfg.ExpiresAt,
		ExpiresSoon:          expiresSoon,
		Expired:              expired,
		LastRefreshAt:        cfg.LastRefreshAt,
		LastRefreshError:     cfg.LastRefreshError,
		UsingCustomOAuthApp:  usingCustom,
		CustomOAuthClientID:  cfg.CustomOAuthClientID,
		HasCustomOAuthSecret: cfg.CustomOAuthClientSecret != "",
		Username:             cfg.Username,
		Nickname:             cfg.Nickname,
		NSFWMode:             cfg.NSFWMode,
		PrivateDefault:       cfg.PrivateDefault,
		Records:              records,
	}, nil
}

func (tm *trackerManager) SeriesSummary(ctx context.Context, subjectID int64) *SeriesTrackerSummary {
	if tm == nil || subjectID <= 0 {
		return nil
	}
	cfg := tm.loadConfig(ctx)
	rec, found, err := tm.svc.store.BangumiTrackerRecordBySubject(ctx, subjectID)
	if err != nil {
		return nil
	}
	enabled := cfg.Enabled && cfg.Token != ""
	if !enabled && !found {
		return nil
	}
	if !found {
		return &SeriesTrackerSummary{
			Enabled: enabled,
			Status:  "idle",
		}
	}
	return &SeriesTrackerSummary{
		Enabled:         enabled,
		Status:          rec.Status,
		RemoteType:      rec.RemoteType,
		RemoteVolStatus: rec.RemoteVolStatus,
		RemotePrivate:   rec.RemotePrivate,
		LastError:       rec.LastError,
		LastSyncedAt:    rec.LastSyncedAt,
	}
}

func (tm *trackerManager) UpdateConfig(ctx context.Context, upd TrackerConfigUpdate) (TrackerStatus, error) {
	cfg := tm.loadConfig(ctx)
	scope, hasScope := database.UserScopeFromContext(ctx)
	canEditGlobalApp := !hasScope || scope.IsAdmin

	if upd.NSFWMode != nil {
		mode, err := normalizeTrackerNSFWMode(*upd.NSFWMode)
		if err != nil {
			return TrackerStatus{}, err
		}
		cfg.NSFWMode = mode
	}
	if upd.PrivateDefault != nil {
		cfg.PrivateDefault = *upd.PrivateDefault
	}

	if canEditGlobalApp {
		if upd.ClearCustomOAuthApp {
			for _, k := range []string{SettingTrackerOAuthClientID, SettingTrackerOAuthClientSecret} {
				if err := tm.svc.store.SetSetting(ctx, k, ""); err != nil {
					return TrackerStatus{}, err
				}
			}
			cfg.CustomOAuthClientID = ""
			cfg.CustomOAuthClientSecret = ""
		} else if upd.CustomOAuthClientID != nil || upd.CustomOAuthClientSecret != nil {
			nextID := cfg.CustomOAuthClientID
			nextSecret := cfg.CustomOAuthClientSecret
			if upd.CustomOAuthClientID != nil {
				nextID = strings.TrimSpace(*upd.CustomOAuthClientID)
			}
			if upd.CustomOAuthClientSecret != nil {
				if s := strings.TrimSpace(*upd.CustomOAuthClientSecret); s != "" {
					nextSecret = s
				}
			}
			if nextID == "" && nextSecret == "" {
				_ = tm.svc.store.SetSetting(ctx, SettingTrackerOAuthClientID, "")
				_ = tm.svc.store.SetSetting(ctx, SettingTrackerOAuthClientSecret, "")
				cfg.CustomOAuthClientID = ""
				cfg.CustomOAuthClientSecret = ""
			} else {
				if nextID == "" || nextSecret == "" {
					return TrackerStatus{}, errors.New("自定义 Bangumi OAuth 应用需同时填写 App ID 和 App Secret")
				}
				if err := tm.svc.store.SetSetting(ctx, SettingTrackerOAuthClientID, nextID); err != nil {
					return TrackerStatus{}, err
				}
				if err := tm.svc.store.SetSetting(ctx, SettingTrackerOAuthClientSecret, nextSecret); err != nil {
					return TrackerStatus{}, err
				}
				cfg.CustomOAuthClientID = nextID
				cfg.CustomOAuthClientSecret = nextSecret
			}
		}
	}

	if upd.ClearToken {
		cfg.Token = ""
		cfg.AuthMode = ""
		cfg.RefreshToken = ""
		cfg.ExpiresAt = ""
		cfg.LastRefreshAt = ""
		cfg.LastRefreshError = ""
		cfg.OAuthRedirectURI = ""
		cfg.Username = ""
		cfg.Nickname = ""
		cfg.UserID = ""
		cfg.Enabled = false
		if err := tm.saveUserConfig(ctx, cfg); err != nil {
			return TrackerStatus{}, err
		}
		return tm.Status(ctx, 30)
	}

	if upd.AccessToken != nil {
		tok := strings.TrimSpace(*upd.AccessToken)
		if tok != "" {
			user, err := tm.VerifyToken(ctx, tok)
			if err != nil {
				return TrackerStatus{}, err
			}
			expiresAt := ""
			if ts, ok := tm.QueryTokenStatus(ctx, tok); ok {
				expiresAt = ts.UTC().Format(time.RFC3339)
			}
			cfg.Token = tok
			cfg.AuthMode = TrackerAuthModePAT
			cfg.RefreshToken = ""
			cfg.OAuthRedirectURI = ""
			cfg.ExpiresAt = expiresAt
			cfg.LastRefreshAt = ""
			cfg.LastRefreshError = ""
			cfg.Username = user.Username
			cfg.Nickname = user.Nickname
			cfg.UserID = strconv.FormatInt(user.ID, 10)
		}
	}

	if upd.Enabled != nil {
		if *upd.Enabled && cfg.Token == "" {
			return TrackerStatus{}, ErrTrackerNotBound
		}
		cfg.Enabled = *upd.Enabled
	}

	if err := tm.saveUserConfig(ctx, cfg); err != nil {
		return TrackerStatus{}, err
	}
	return tm.Status(ctx, 30)
}

func (tm *trackerManager) TestToken(ctx context.Context, candidateToken *string) TrackerTestResult {
	testingSaved := candidateToken == nil || strings.TrimSpace(*candidateToken) == ""
	tok := ""
	var savedCfg trackerConfig
	if !testingSaved {
		tok = strings.TrimSpace(*candidateToken)
	} else {
		savedCfg = tm.loadConfig(ctx)
		if savedCfg.AuthMode == TrackerAuthModeOAuth && savedCfg.RefreshToken != "" {
			savedCfg, _ = tm.ensureFreshOAuthToken(ctx, savedCfg)
		}
		tok = savedCfg.Token
	}
	if tok == "" {
		return TrackerTestResult{OK: false, Error: ErrTrackerNotBound.Error()}
	}
	start := time.Now()
	user, err := tm.VerifyToken(ctx, tok)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return TrackerTestResult{OK: false, LatencyMs: latency, Error: err.Error()}
	}
	expiresAt := ""
	if ts, ok := tm.QueryTokenStatus(ctx, tok); ok {
		expiresAt = ts.UTC().Format(time.RFC3339)
	}
	if testingSaved {
		savedCfg.Username = user.Username
		savedCfg.Nickname = user.Nickname
		savedCfg.UserID = strconv.FormatInt(user.ID, 10)
		if expiresAt != "" {
			savedCfg.ExpiresAt = expiresAt
		}
		_ = tm.saveUserConfig(ctx, savedCfg)
	}
	return TrackerTestResult{
		OK:        true,
		Username:  user.Username,
		Nickname:  user.Nickname,
		ExpiresAt: expiresAt,
		LatencyMs: latency,
	}
}

func (tm *trackerManager) VerifyToken(ctx context.Context, token string) (TrackerUser, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return TrackerUser{}, ErrTrackerNotBound
	}
	endpoint := strings.TrimRight(tm.svc.live.baseURL, "/") + "/v0/me"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return TrackerUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", archiveUserAgent)
	req.Header.Set("Accept", "application/json")

	client, proxyRaw := tm.svc.live.httpClient()
	resp, err := client.Do(req)
	if err != nil {
		return TrackerUser{}, errors.New(SanitizeProxyError(err, proxyRaw))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return TrackerUser{}, ErrTrackerTokenInvalid
	}
	if resp.StatusCode != http.StatusOK {
		return TrackerUser{}, fmt.Errorf("Bangumi /v0/me returned HTTP %d", resp.StatusCode)
	}
	var user TrackerUser
	if err := json.Unmarshal(body, &user); err != nil {
		return TrackerUser{}, fmt.Errorf("parse Bangumi user profile: %w", err)
	}
	if user.ID <= 0 && strings.TrimSpace(user.Username) == "" {
		return TrackerUser{}, ErrTrackerTokenInvalid
	}
	return user, nil
}

type remoteCollectionState struct {
	Type      int  `json:"type"`
	VolStatus int  `json:"vol_status"`
	EpStatus  int  `json:"ep_status"`
	Private   bool `json:"private"`
}

// SyncSeries pushes monotonic reading progress for a single series to Bangumi.
// When manualRetry is false, automatic sync requires tracker to be enabled and skips no-op pushes.
func (tm *trackerManager) SyncSeries(ctx context.Context, seriesID string, manualRetry bool) (database.BangumiTrackerRecord, error) {
	cfg := tm.loadConfig(ctx)
	if cfg.Token == "" {
		if manualRetry {
			return database.BangumiTrackerRecord{}, ErrTrackerNotBound
		}
		return database.BangumiTrackerRecord{}, nil
	}
	if !cfg.Enabled && !manualRetry {
		return database.BangumiTrackerRecord{}, nil
	}

	snap, ok, err := tm.svc.store.BangumiTrackerSeriesSnapshot(ctx, seriesID)
	if err != nil || !ok {
		return database.BangumiTrackerRecord{}, err
	}
	if snap.BgmSubjectID <= 0 {
		if manualRetry {
			return database.BangumiTrackerRecord{}, ErrTrackerUnboundItem
		}
		return database.BangumiTrackerRecord{}, nil
	}
	if !snap.HasAnyProgress {
		return database.BangumiTrackerRecord{}, nil
	}

	nowStr := tm.svc.now().UTC().Format(time.RFC3339)

	// NSFW policy: skip by default, or sync as private: true.
	wantPrivate := cfg.PrivateDefault
	if snap.IsNSFW {
		if cfg.NSFWMode == TrackerNSFWModeSkip {
			rec := database.BangumiTrackerRecord{
				BgmSubjectID: snap.BgmSubjectID,
				SeriesID:     snap.SeriesID,
				SeriesTitle:  snap.SeriesTitle,
				Status:       "skipped",
				LastLocalVol: snap.MaxCompletedOrdinal,
				LastError:    "NSFW 作品已按设置跳过同步",
				UpdatedAt:    nowStr,
			}
			_ = tm.svc.store.UpsertBangumiTrackerRecord(ctx, rec)
			return rec, nil
		}
		wantPrivate = true
	}

	// Compute desired local state (monotonic vol_status, clamped to known total_book_count).
	desiredVol := snap.MaxCompletedOrdinal
	totalKnown := 0
	if snap.TotalBookCount != nil && *snap.TotalBookCount > 0 {
		totalKnown = *snap.TotalBookCount
	}
	if totalKnown > 0 && desiredVol > totalKnown {
		desiredVol = totalKnown
	}
	desiredType := CollectionTypeDoing
	if snap.OneShot && snap.AllCompleted {
		desiredType = CollectionTypeCollect
		if desiredVol < 1 {
			desiredVol = 1
		}
		if totalKnown > 0 && desiredVol > totalKnown {
			desiredVol = totalKnown
		}
	} else if snap.AllCompleted && strings.EqualFold(snap.SeriesStatus, "ENDED") {
		if totalKnown <= 0 || desiredVol >= totalKnown {
			desiredType = CollectionTypeCollect
		}
	}

	existingRec, hasExisting, _ := tm.svc.store.BangumiTrackerRecordBySubject(ctx, snap.BgmSubjectID)
	if !manualRetry && hasExisting && existingRec.Status == "synced" {
		volNoop := desiredVol <= existingRec.RemoteVolStatus
		typeNoop := desiredType == existingRec.RemoteType ||
			existingRec.RemoteType == CollectionTypeCollect ||
			existingRec.RemoteType == CollectionTypeOnHold ||
			existingRec.RemoteType == CollectionTypeDropped
		if volNoop && typeNoop {
			return existingRec, nil
		}
	}

	// Proactive OAuth refresh if expiring within 24h.
	refreshedOnce := false
	if cfg.AuthMode == TrackerAuthModeOAuth && cfg.RefreshToken != "" {
		if nextCfg, rErr := tm.ensureFreshOAuthToken(ctx, cfg); rErr == nil {
			if nextCfg.Token != cfg.Token {
				refreshedOnce = true
			}
			cfg = nextCfg
		}
	}
	tryRefreshAfter401 := func() bool {
		if refreshedOnce || cfg.AuthMode != TrackerAuthModeOAuth || cfg.RefreshToken == "" {
			return false
		}
		refreshedOnce = true
		if _, rErr := tm.RefreshOAuthToken(ctx, true); rErr != nil {
			return false
		}
		cfg = tm.loadConfig(ctx)
		return cfg.Token != ""
	}

	// Ensure userPathSegment is available (backfill via /v0/me if missing).
	userKey := cfg.userPathSegment()
	if userKey == "" {
		user, uErr := tm.VerifyToken(ctx, cfg.Token)
		if errors.Is(uErr, ErrTrackerTokenInvalid) && tryRefreshAfter401() {
			user, uErr = tm.VerifyToken(ctx, cfg.Token)
		}
		if uErr != nil {
			return tm.saveFailure(ctx, snap, desiredVol, existingRec, nowStr, uErr)
		}
		cfg.Username = user.Username
		cfg.Nickname = user.Nickname
		cfg.UserID = strconv.FormatInt(user.ID, 10)
		_ = tm.saveUserConfig(ctx, cfg)
		userKey = cfg.userPathSegment()
	}

	if err := tm.limiter.Wait(ctx); err != nil {
		return database.BangumiTrackerRecord{}, err
	}

	remote, exists, err := tm.fetchRemoteCollection(ctx, cfg.Token, userKey, snap.BgmSubjectID)
	if errors.Is(err, ErrTrackerTokenInvalid) && tryRefreshAfter401() {
		remote, exists, err = tm.fetchRemoteCollection(ctx, cfg.Token, userKey, snap.BgmSubjectID)
	}
	if err != nil {
		return tm.saveFailure(ctx, snap, desiredVol, existingRec, nowStr, err)
	}

	finalType := desiredType
	finalVol := desiredVol
	finalPrivate := wantPrivate

	if !exists {
		payload := map[string]any{
			"type":    desiredType,
			"private": wantPrivate,
		}
		if desiredVol > 0 {
			payload["vol_status"] = desiredVol
		}
		err = tm.pushCollection(ctx, http.MethodPost, cfg.Token, snap.BgmSubjectID, payload)
		if errors.Is(err, ErrTrackerTokenInvalid) && tryRefreshAfter401() {
			err = tm.pushCollection(ctx, http.MethodPost, cfg.Token, snap.BgmSubjectID, payload)
		}
		if err != nil {
			return tm.saveFailure(ctx, snap, desiredVol, existingRec, nowStr, err)
		}
	} else {
		finalType = remote.Type
		finalVol = remote.VolStatus
		finalPrivate = remote.Private

		patch := map[string]any{}
		// Only promote 想读 -> 在读/读过, or 在读 -> 读过; never downgrade 读过/搁置/抛弃.
		if remote.Type == CollectionTypeWish {
			patch["type"] = desiredType
			finalType = desiredType
		} else if remote.Type == CollectionTypeDoing && desiredType == CollectionTypeCollect {
			patch["type"] = CollectionTypeCollect
			finalType = CollectionTypeCollect
		}
		// Strictly monotonic vol_status.
		if desiredVol > remote.VolStatus {
			patch["vol_status"] = desiredVol
			finalVol = desiredVol
		}
		if len(patch) > 0 {
			err = tm.pushCollection(ctx, http.MethodPatch, cfg.Token, snap.BgmSubjectID, patch)
			if errors.Is(err, ErrTrackerTokenInvalid) && tryRefreshAfter401() {
				err = tm.pushCollection(ctx, http.MethodPatch, cfg.Token, snap.BgmSubjectID, patch)
			}
			if err != nil {
				return tm.saveFailure(ctx, snap, desiredVol, existingRec, nowStr, err)
			}
		}
	}

	syncedAt := nowStr
	rec := database.BangumiTrackerRecord{
		BgmSubjectID:    snap.BgmSubjectID,
		SeriesID:        snap.SeriesID,
		SeriesTitle:     snap.SeriesTitle,
		Status:          "synced",
		RemoteType:      finalType,
		RemoteVolStatus: finalVol,
		RemotePrivate:   finalPrivate,
		LastLocalVol:    desiredVol,
		LastError:       "",
		LastSyncedAt:    &syncedAt,
		UpdatedAt:       nowStr,
	}
	_ = tm.svc.store.UpsertBangumiTrackerRecord(ctx, rec)
	diagnostics.Default().RecordVerbose(diagnostics.Event{
		Level:     diagnostics.LevelInfo,
		Component: "bangumi",
		Stage:     "tracker",
		Action:    "synced",
		Detail:    fmt.Sprintf("synced subject #%d (type=%d vol=%d)", snap.BgmSubjectID, finalType, finalVol),
	})
	return rec, nil
}

func (tm *trackerManager) saveFailure(
	ctx context.Context,
	snap database.BangumiTrackerSeriesSnapshot,
	desiredVol int,
	prev database.BangumiTrackerRecord,
	nowStr string,
	syncErr error,
) (database.BangumiTrackerRecord, error) {
	_, proxyRaw := tm.svc.live.httpClient()
	cleanErr := SanitizeProxyError(syncErr, proxyRaw)
	rec := database.BangumiTrackerRecord{
		BgmSubjectID:    snap.BgmSubjectID,
		SeriesID:        snap.SeriesID,
		SeriesTitle:     snap.SeriesTitle,
		Status:          "failed",
		RemoteType:      prev.RemoteType,
		RemoteVolStatus: prev.RemoteVolStatus,
		RemotePrivate:   prev.RemotePrivate,
		LastLocalVol:    desiredVol,
		LastError:       cleanErr,
		LastSyncedAt:    prev.LastSyncedAt,
		UpdatedAt:       nowStr,
	}
	_ = tm.svc.store.UpsertBangumiTrackerRecord(ctx, rec)
	diagnostics.Default().Record(diagnostics.Event{
		Level:     diagnostics.LevelWarn,
		Component: "bangumi",
		Stage:     "tracker",
		Action:    "sync_failed",
		Code:      diagnostics.ClassifyError(syncErr),
		Detail:    cleanErr,
	})
	return rec, errors.New(cleanErr)
}

func (tm *trackerManager) fetchRemoteCollection(
	ctx context.Context,
	token, userKey string,
	subjectID int64,
) (remoteCollectionState, bool, error) {
	endpoint := fmt.Sprintf("%s/v0/users/%s/collections/%d",
		strings.TrimRight(tm.svc.live.baseURL, "/"),
		url.PathEscape(userKey),
		subjectID,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return remoteCollectionState{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", archiveUserAgent)
	req.Header.Set("Accept", "application/json")

	client, proxyRaw := tm.svc.live.httpClient()
	resp, err := client.Do(req)
	if err != nil {
		return remoteCollectionState{}, false, errors.New(SanitizeProxyError(err, proxyRaw))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode == http.StatusNotFound {
		return remoteCollectionState{}, false, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return remoteCollectionState{}, false, ErrTrackerTokenInvalid
	}
	if resp.StatusCode != http.StatusOK {
		return remoteCollectionState{}, false, fmt.Errorf("Bangumi collection GET returned HTTP %d", resp.StatusCode)
	}
	var state remoteCollectionState
	if err := json.Unmarshal(body, &state); err != nil {
		return remoteCollectionState{}, false, fmt.Errorf("parse Bangumi collection: %w", err)
	}
	return state, true, nil
}

func (tm *trackerManager) pushCollection(
	ctx context.Context,
	method, token string,
	subjectID int64,
	payload map[string]any,
) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/v0/users/-/collections/%d",
		strings.TrimRight(tm.svc.live.baseURL, "/"),
		subjectID,
	)
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", archiveUserAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client, proxyRaw := tm.svc.live.httpClient()
	resp, err := client.Do(req)
	if err != nil {
		return errors.New(SanitizeProxyError(err, proxyRaw))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrTrackerTokenInvalid
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Bangumi collection %s returned HTTP %d", method, resp.StatusCode)
	}
	return nil
}
