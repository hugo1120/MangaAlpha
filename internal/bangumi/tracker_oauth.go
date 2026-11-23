package bangumi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xJogger/fake-komga-115/internal/diagnostics"
)

const (
	// Official public MangaAlpha Bangumi OAuth app credentials (blank callback URL in bgm.tv/dev/app).
	// Users may override these with their own custom Bangumi OAuth app in Admin settings.
	DefaultOAuthClientID     = "bgm76156b038da30df08"
	DefaultOAuthClientSecret = "782ab4a52a5d9a9994ea75fed1d8cc81"

	defaultOAuthBaseURL = "https://bgm.tv"

	SettingTrackerAuthMode          = "bangumi_tracker_auth_mode"
	SettingTrackerRefreshToken      = "bangumi_tracker_refresh_token"
	SettingTrackerExpiresAt         = "bangumi_tracker_expires_at"
	SettingTrackerLastRefreshAt     = "bangumi_tracker_last_refresh_at"
	SettingTrackerLastRefreshError  = "bangumi_tracker_last_refresh_error"
	SettingTrackerOAuthRedirectURI  = "bangumi_tracker_oauth_redirect_uri"
	SettingTrackerOAuthClientID     = "bangumi_tracker_oauth_client_id"
	SettingTrackerOAuthClientSecret = "bangumi_tracker_oauth_client_secret"

	TrackerAuthModeOAuth = "oauth"
	TrackerAuthModePAT   = "pat"

	oauthBackgroundCheckInterval = 12 * time.Hour
	oauthBackgroundRefreshWindow = 72 * time.Hour
	oauthSyncRefreshWindow       = 24 * time.Hour
	patWarnSoonWindow            = 30 * 24 * time.Hour
)

var (
	ErrOAuthNotConfigured  = errors.New("当前未使用 Bangumi OAuth 授权或缺少 refresh_token")
	ErrOAuthExchangeFailed = errors.New("Bangumi OAuth 授权换取令牌失败")
)

type oauthTokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int64  `json:"expires_in"`
	TokenType        string `json:"token_type"`
	RefreshToken     string `json:"refresh_token"`
	UserID           int64  `json:"user_id"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type oauthTokenStatusResponse struct {
	AccessToken string `json:"access_token"`
	ClientID    string `json:"client_id"`
	Expires     int64  `json:"expires"`
	UserID      int64  `json:"user_id"`
}

func (tm *trackerManager) oauthBase() string {
	tm.mu.Lock()
	base := strings.TrimSpace(tm.oauthBaseURL)
	tm.mu.Unlock()
	if base == "" {
		return defaultOAuthBaseURL
	}
	return strings.TrimRight(base, "/")
}

func (tm *trackerManager) SetOAuthBaseURL(baseURL string) {
	if tm == nil {
		return
	}
	tm.mu.Lock()
	tm.oauthBaseURL = strings.TrimSpace(baseURL)
	tm.mu.Unlock()
}

func (tm *trackerManager) effectiveOAuthApp(cfg trackerConfig) (clientID, clientSecret string, custom bool) {
	if strings.TrimSpace(cfg.CustomOAuthClientID) != "" && strings.TrimSpace(cfg.CustomOAuthClientSecret) != "" {
		return strings.TrimSpace(cfg.CustomOAuthClientID), strings.TrimSpace(cfg.CustomOAuthClientSecret), true
	}
	return DefaultOAuthClientID, DefaultOAuthClientSecret, false
}

// BuildOAuthAuthorizeURL constructs the Bangumi OAuth authorization URL using the active app credentials.
func (tm *trackerManager) BuildOAuthAuthorizeURL(ctx context.Context, redirectURI, state string) (string, error) {
	cfg := tm.loadConfig(ctx)
	clientID, _, _ := tm.effectiveOAuthApp(cfg)
	if clientID == "" {
		return "", errors.New("Bangumi OAuth App ID is not configured")
	}
	u, err := url.Parse(tm.oauthBase() + "/oauth/authorize")
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", clientID)
	q.Set("response_type", "code")
	if strings.TrimSpace(redirectURI) != "" {
		q.Set("redirect_uri", strings.TrimSpace(redirectURI))
	}
	if strings.TrimSpace(state) != "" {
		q.Set("state", strings.TrimSpace(state))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ExchangeOAuthCode trades an authorization code for access_token + refresh_token and persists them.
func (tm *trackerManager) ExchangeOAuthCode(ctx context.Context, code, redirectURI string) (TrackerStatus, error) {
	code = strings.TrimSpace(code)
	redirectURI = strings.TrimSpace(redirectURI)
	if code == "" {
		return TrackerStatus{}, fmt.Errorf("%w: authorization code is empty", ErrOAuthExchangeFailed)
	}
	cfg := tm.loadConfig(ctx)
	clientID, clientSecret, _ := tm.effectiveOAuthApp(cfg)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", code)
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}

	tokResp, err := tm.postOAuthToken(ctx, form)
	if err != nil {
		return TrackerStatus{}, err
	}
	if strings.TrimSpace(tokResp.AccessToken) == "" {
		return TrackerStatus{}, fmt.Errorf("%w: empty access_token in response", ErrOAuthExchangeFailed)
	}

	user, err := tm.VerifyToken(ctx, tokResp.AccessToken)
	if err != nil {
		return TrackerStatus{}, err
	}

	now := tm.svc.now().UTC()
	expiresAt := ""
	if tokResp.ExpiresIn > 0 {
		expiresAt = now.Add(time.Duration(tokResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	} else if ts, ok := tm.QueryTokenStatus(ctx, tokResp.AccessToken); ok {
		expiresAt = ts.UTC().Format(time.RFC3339)
	}

	tm.refreshMu.Lock()
	defer tm.refreshMu.Unlock()

	if err := tm.svc.store.SetSetting(ctx, SettingTrackerToken, strings.TrimSpace(tokResp.AccessToken)); err != nil {
		return TrackerStatus{}, err
	}
	_ = tm.svc.store.SetSetting(ctx, SettingTrackerRefreshToken, strings.TrimSpace(tokResp.RefreshToken))
	_ = tm.svc.store.SetSetting(ctx, SettingTrackerAuthMode, TrackerAuthModeOAuth)
	_ = tm.svc.store.SetSetting(ctx, SettingTrackerOAuthRedirectURI, redirectURI)
	_ = tm.svc.store.SetSetting(ctx, SettingTrackerExpiresAt, expiresAt)
	_ = tm.svc.store.SetSetting(ctx, SettingTrackerLastRefreshAt, now.Format(time.RFC3339))
	_ = tm.svc.store.SetSetting(ctx, SettingTrackerLastRefreshError, "")
	_ = tm.svc.store.SetSetting(ctx, SettingTrackerUsername, user.Username)
	_ = tm.svc.store.SetSetting(ctx, SettingTrackerNickname, user.Nickname)
	uid := user.ID
	if uid <= 0 {
		uid = tokResp.UserID
	}
	if uid > 0 {
		_ = tm.svc.store.SetSetting(ctx, SettingTrackerUserID, strconv.FormatInt(uid, 10))
	}
	_ = tm.svc.store.SetSetting(ctx, SettingTrackerEnabled, "true")

	diagnostics.Default().Record(diagnostics.Event{
		Level:     diagnostics.LevelInfo,
		Component: "bangumi",
		Stage:     "oauth",
		Action:    "bound",
		Detail:    "Bangumi OAuth tracker account authorized",
	})
	return tm.Status(ctx, 30)
}

// RefreshOAuthToken rotates the OAuth access_token and refresh_token under a mutex so concurrent
// requests never double-spend the same refresh_token.
func (tm *trackerManager) RefreshOAuthToken(ctx context.Context, force bool) (TrackerStatus, error) {
	tm.refreshMu.Lock()
	defer tm.refreshMu.Unlock()

	cfg := tm.loadConfig(ctx)
	if cfg.AuthMode != TrackerAuthModeOAuth || strings.TrimSpace(cfg.RefreshToken) == "" {
		return TrackerStatus{}, ErrOAuthNotConfigured
	}
	if !force && cfg.ExpiresAt != "" {
		if exp, err := time.Parse(time.RFC3339, cfg.ExpiresAt); err == nil {
			if exp.Sub(tm.svc.now().UTC()) > oauthBackgroundRefreshWindow {
				return tm.Status(ctx, 30)
			}
		}
	}

	clientID, clientSecret, _ := tm.effectiveOAuthApp(cfg)
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", cfg.RefreshToken)
	if strings.TrimSpace(cfg.OAuthRedirectURI) != "" {
		form.Set("redirect_uri", strings.TrimSpace(cfg.OAuthRedirectURI))
	}

	tokResp, err := tm.postOAuthToken(ctx, form)
	if err != nil {
		_, proxyRaw := tm.svc.live.httpClient()
		cleanErr := SanitizeProxyError(err, proxyRaw)
		_ = tm.svc.store.SetSetting(ctx, SettingTrackerLastRefreshError, cleanErr)
		diagnostics.Default().Record(diagnostics.Event{
			Level:     diagnostics.LevelWarn,
			Component: "bangumi",
			Stage:     "oauth",
			Action:    "refresh_failed",
			Code:      diagnostics.ClassifyError(err),
			Detail:    cleanErr,
		})
		return TrackerStatus{}, errors.New(cleanErr)
	}
	if strings.TrimSpace(tokResp.AccessToken) == "" {
		errMsg := "Bangumi OAuth refresh returned empty access_token"
		_ = tm.svc.store.SetSetting(ctx, SettingTrackerLastRefreshError, errMsg)
		return TrackerStatus{}, errors.New(errMsg)
	}

	now := tm.svc.now().UTC()
	expiresAt := ""
	if tokResp.ExpiresIn > 0 {
		expiresAt = now.Add(time.Duration(tokResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	} else if ts, ok := tm.QueryTokenStatus(ctx, tokResp.AccessToken); ok {
		expiresAt = ts.UTC().Format(time.RFC3339)
	}

	if err := tm.svc.store.SetSetting(ctx, SettingTrackerToken, strings.TrimSpace(tokResp.AccessToken)); err != nil {
		return TrackerStatus{}, err
	}
	if strings.TrimSpace(tokResp.RefreshToken) != "" {
		_ = tm.svc.store.SetSetting(ctx, SettingTrackerRefreshToken, strings.TrimSpace(tokResp.RefreshToken))
	}
	_ = tm.svc.store.SetSetting(ctx, SettingTrackerExpiresAt, expiresAt)
	_ = tm.svc.store.SetSetting(ctx, SettingTrackerLastRefreshAt, now.Format(time.RFC3339))
	_ = tm.svc.store.SetSetting(ctx, SettingTrackerLastRefreshError, "")

	// Best-effort profile refresh so username/nickname stay fresh.
	if user, uErr := tm.VerifyToken(ctx, tokResp.AccessToken); uErr == nil {
		_ = tm.svc.store.SetSetting(ctx, SettingTrackerUsername, user.Username)
		_ = tm.svc.store.SetSetting(ctx, SettingTrackerNickname, user.Nickname)
		if user.ID > 0 {
			_ = tm.svc.store.SetSetting(ctx, SettingTrackerUserID, strconv.FormatInt(user.ID, 10))
		}
	}

	diagnostics.Default().RecordVerbose(diagnostics.Event{
		Level:     diagnostics.LevelInfo,
		Component: "bangumi",
		Stage:     "oauth",
		Action:    "refreshed",
		Detail:    "Bangumi OAuth access_token refreshed",
	})
	return tm.Status(ctx, 30)
}

func (tm *trackerManager) postOAuthToken(ctx context.Context, form url.Values) (oauthTokenResponse, error) {
	endpoint := tm.oauthBase() + "/oauth/access_token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return oauthTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", archiveUserAgent)

	client, proxyRaw := tm.svc.live.httpClient()
	resp, err := client.Do(req)
	if err != nil {
		return oauthTokenResponse{}, errors.New(SanitizeProxyError(err, proxyRaw))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))

	var parsed oauthTokenResponse
	_ = json.Unmarshal(body, &parsed)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || parsed.Error != "" {
		desc := strings.TrimSpace(parsed.ErrorDescription)
		if desc == "" {
			desc = strings.TrimSpace(parsed.Error)
		}
		if desc == "" {
			desc = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		if strings.EqualFold(parsed.Error, "invalid_grant") {
			return oauthTokenResponse{}, fmt.Errorf("%w: %s（授权码或 Refresh Token 已失效，请重新点击 Bangumi OAuth 授权）", ErrOAuthExchangeFailed, desc)
		}
		return oauthTokenResponse{}, fmt.Errorf("%w: %s", ErrOAuthExchangeFailed, desc)
	}
	return parsed, nil
}

// QueryTokenStatus queries Bangumi's /oauth/token_status endpoint (best-effort) to discover token expiry.
func (tm *trackerManager) QueryTokenStatus(ctx context.Context, token string) (time.Time, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return time.Time{}, false
	}
	form := url.Values{}
	form.Set("access_token", token)
	endpoint := tm.oauthBase() + "/oauth/token_status"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return time.Time{}, false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", archiveUserAgent)

	client, _ := tm.svc.live.httpClient()
	resp, err := client.Do(req)
	if err != nil {
		return time.Time{}, false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	if resp.StatusCode != http.StatusOK {
		return time.Time{}, false
	}
	var st oauthTokenStatusResponse
	if err := json.Unmarshal(body, &st); err != nil || st.Expires <= 0 {
		return time.Time{}, false
	}
	return time.Unix(st.Expires, 0).UTC(), true
}

func (tm *trackerManager) ensureFreshOAuthToken(ctx context.Context, cfg trackerConfig) (trackerConfig, error) {
	if cfg.AuthMode != TrackerAuthModeOAuth || strings.TrimSpace(cfg.RefreshToken) == "" {
		return cfg, nil
	}
	needsRefresh := false
	if cfg.ExpiresAt == "" {
		needsRefresh = true
	} else if exp, err := time.Parse(time.RFC3339, cfg.ExpiresAt); err == nil {
		if exp.Sub(tm.svc.now().UTC()) <= oauthSyncRefreshWindow {
			needsRefresh = true
		}
	}
	if !needsRefresh {
		return cfg, nil
	}
	if _, err := tm.RefreshOAuthToken(ctx, true); err != nil {
		return cfg, err
	}
	return tm.loadConfig(ctx), nil
}

func (tm *trackerManager) refreshLoop() {
	defer close(tm.refreshDone)
	startupTimer := time.NewTimer(30 * time.Second)
	defer startupTimer.Stop()
	ticker := time.NewTicker(oauthBackgroundCheckInterval)
	defer ticker.Stop()

	check := func() {
		checkCtx, cancel := context.WithTimeout(tm.svc.ctx, 30*time.Second)
		defer cancel()
		cfg := tm.loadConfig(checkCtx)
		if cfg.AuthMode != TrackerAuthModeOAuth || strings.TrimSpace(cfg.RefreshToken) == "" {
			return
		}
		_, _ = tm.RefreshOAuthToken(checkCtx, false)
	}

	for {
		select {
		case <-tm.svc.ctx.Done():
			return
		case <-startupTimer.C:
			check()
		case <-ticker.C:
			check()
		}
	}
}
