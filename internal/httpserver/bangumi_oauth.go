package httpserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/database"
)

const (
	bangumiOAuthStateTTL     = 10 * time.Minute
	bangumiOAuthMaxStates    = 32
	bangumiOAuthCallbackPath = "/admin/api/bangumi/tracker/oauth/callback"
)

type bangumiOAuthPending struct {
	UserID      string
	IsAdmin     bool
	CallbackURL string
	Origin      string
	ExpiresAt   time.Time
}

type bangumiOAuthStateStore struct {
	mu    sync.Mutex
	items map[string]bangumiOAuthPending
}

func newBangumiOAuthStateStore() *bangumiOAuthStateStore {
	return &bangumiOAuthStateStore{
		items: make(map[string]bangumiOAuthPending),
	}
}

func (st *bangumiOAuthStateStore) Create(userID string, isAdmin bool, origin, callbackURL string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	state := hex.EncodeToString(raw[:])
	now := time.Now()
	if strings.TrimSpace(userID) == "" {
		userID = database.PrimaryAdminUserID
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	for k, v := range st.items {
		if now.After(v.ExpiresAt) {
			delete(st.items, k)
		}
	}
	for len(st.items) >= bangumiOAuthMaxStates {
		var oldestKey string
		var oldestExp time.Time
		for k, v := range st.items {
			if oldestKey == "" || v.ExpiresAt.Before(oldestExp) {
				oldestKey = k
				oldestExp = v.ExpiresAt
			}
		}
		if oldestKey == "" {
			break
		}
		delete(st.items, oldestKey)
	}
	st.items[state] = bangumiOAuthPending{
		UserID:      userID,
		IsAdmin:     isAdmin,
		CallbackURL: callbackURL,
		Origin:      origin,
		ExpiresAt:   now.Add(bangumiOAuthStateTTL),
	}
	return state, nil
}

func (st *bangumiOAuthStateStore) Consume(state string) (bangumiOAuthPending, bool) {
	state = strings.TrimSpace(state)
	if state == "" {
		return bangumiOAuthPending{}, false
	}
	now := time.Now()
	st.mu.Lock()
	defer st.mu.Unlock()
	item, ok := st.items[state]
	if !ok {
		return bangumiOAuthPending{}, false
	}
	delete(st.items, state)
	if now.After(item.ExpiresAt) {
		return bangumiOAuthPending{}, false
	}
	return item, true
}

func isBangumiOAuthCallbackPath(p, method string) bool {
	return method == http.MethodGet && p == bangumiOAuthCallbackPath
}

func (s *Server) startBangumiTrackerOAuth(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusServiceUnavailable, "BANGUMI_UNAVAILABLE", "Bangumi service is not initialized.")
		return
	}
	origin := s.requestOrigin(r)
	if origin == "" {
		writeError(w, http.StatusBadRequest, "INVALID_ORIGIN", "Unable to determine server origin from Host header.")
		return
	}
	principal, _ := s.currentPrincipal(r)
	callbackURL := origin + bangumiOAuthCallbackPath
	state, err := s.bgmOAuthStates.Create(principal.UserID, principal.IsAdmin, origin, callbackURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "OAUTH_STATE_FAILED", "Failed to initialize OAuth authorization state.")
		return
	}
	authorizeURL, err := s.bangumi.BuildTrackerOAuthAuthorizeURL(r.Context(), callbackURL, state)
	if err != nil {
		writeError(w, http.StatusBadRequest, "OAUTH_CONFIG_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authorizeUrl":     authorizeURL,
		"callbackUrl":      callbackURL,
		"state":            state,
		"expiresInSeconds": int(bangumiOAuthStateTTL.Seconds()),
	})
}

func (s *Server) bangumiTrackerOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeBangumiOAuthCallbackPage(w, http.StatusServiceUnavailable, "", true, false, "Bangumi 服务未初始化。")
		return
	}
	q := r.URL.Query()
	state := strings.TrimSpace(q.Get("state"))
	pending, ok := s.bgmOAuthStates.Consume(state)
	if !ok {
		writeBangumiOAuthCallbackPage(w, http.StatusBadRequest, "", true, false, "授权状态已过期或无效，请回到页面重新点击「Bangumi OAuth 一键授权」。")
		return
	}
	if errCode := strings.TrimSpace(q.Get("error")); errCode != "" {
		desc := strings.TrimSpace(q.Get("error_description"))
		if desc == "" {
			desc = errCode
		}
		writeBangumiOAuthCallbackPage(w, http.StatusBadRequest, pending.Origin, pending.IsAdmin, false, "Bangumi 授权未完成："+desc)
		return
	}
	code := strings.TrimSpace(q.Get("code"))
	if code == "" {
		writeBangumiOAuthCallbackPage(w, http.StatusBadRequest, pending.Origin, pending.IsAdmin, false, "回调缺少授权码（code），请重新发起授权。")
		return
	}
	userCtx := database.WithUserID(r.Context(), pending.UserID)
	st, err := s.bangumi.ExchangeTrackerOAuthCode(userCtx, code, pending.CallbackURL)
	if err != nil {
		writeBangumiOAuthCallbackPage(w, http.StatusBadGateway, pending.Origin, pending.IsAdmin, false, err.Error())
		return
	}
	name := st.Nickname
	if strings.TrimSpace(name) == "" {
		name = st.Username
	}
	msg := "已成功绑定 Bangumi 账号并开启自动续期"
	if name != "" {
		msg = fmt.Sprintf("已成功绑定 Bangumi 账号「%s」并开启自动续期", name)
	}
	writeBangumiOAuthCallbackPage(w, http.StatusOK, pending.Origin, pending.IsAdmin, true, msg)
}

func (s *Server) exchangeBangumiTrackerOAuthCode(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusServiceUnavailable, "BANGUMI_UNAVAILABLE", "Bangumi service is not initialized.")
		return
	}
	var req struct {
		CodeOrURL string `json:"codeOrUrl"`
		State     string `json:"state"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	raw := strings.TrimSpace(req.CodeOrURL)
	if raw == "" {
		writeError(w, http.StatusBadRequest, "EMPTY_OAUTH_CODE", "请粘贴完整的回调链接或授权码 code。")
		return
	}
	code := raw
	state := strings.TrimSpace(req.State)
	if strings.Contains(raw, "://") || strings.Contains(raw, "code=") {
		parseTarget := raw
		if !strings.Contains(parseTarget, "://") {
			parseTarget = "http://local/?" + strings.TrimPrefix(parseTarget, "?")
		}
		if u, err := url.Parse(parseTarget); err == nil {
			if c := strings.TrimSpace(u.Query().Get("code")); c != "" {
				code = c
			}
			if stVal := strings.TrimSpace(u.Query().Get("state")); stVal != "" {
				state = stVal
			}
		}
	}
	principal, _ := s.currentPrincipal(r)
	callbackURL := ""
	if pending, ok := s.bgmOAuthStates.Consume(state); ok {
		if pending.UserID != "" && principal.UserID != "" && pending.UserID != principal.UserID {
			writeError(w, http.StatusForbidden, "OAUTH_STATE_MISMATCH", "该授权状态属于其他账号，请使用当前账号重新发起授权。")
			return
		}
		callbackURL = pending.CallbackURL
	} else if origin := s.requestOrigin(r); origin != "" {
		callbackURL = origin + bangumiOAuthCallbackPath
	}
	st, err := s.bangumi.ExchangeTrackerOAuthCode(r.Context(), code, callbackURL)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, bangumi.ErrOAuthExchangeFailed) || errors.Is(err, bangumi.ErrTrackerTokenInvalid) {
			status = http.StatusBadRequest
		}
		writeError(w, status, "OAUTH_EXCHANGE_FAILED", err.Error())
		return
	}
	if !principal.IsAdmin {
		st.CustomOAuthClientID = ""
		st.HasCustomOAuthSecret = false
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) refreshBangumiTrackerOAuth(w http.ResponseWriter, r *http.Request) {
	if s.bangumi == nil {
		writeError(w, http.StatusServiceUnavailable, "BANGUMI_UNAVAILABLE", "Bangumi service is not initialized.")
		return
	}
	st, err := s.bangumi.RefreshTrackerOAuthNow(r.Context())
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, bangumi.ErrOAuthNotConfigured) {
			status = http.StatusBadRequest
		}
		writeError(w, status, "OAUTH_REFRESH_FAILED", err.Error())
		return
	}
	if principal, ok := s.currentPrincipal(r); ok && !principal.IsAdmin {
		st.CustomOAuthClientID = ""
		st.HasCustomOAuthSecret = false
	}
	writeJSON(w, http.StatusOK, st)
}

func writeBangumiOAuthCallbackPage(w http.ResponseWriter, status int, targetOrigin string, isAdmin bool, ok bool, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)

	title := "Bangumi 授权成功"
	accent := "#16a34a"
	icon := "✅"
	if !ok {
		title = "Bangumi 授权失败"
		accent = "#dc2626"
		icon = "❌"
	}
	payloadJSON, _ := json.Marshal(map[string]any{
		"type":    "MANGAALPHA_BGM_OAUTH_DONE",
		"ok":      ok,
		"message": message,
	})
	originJSON, _ := json.Marshal(targetOrigin)
	backHref := "/admin?tab=bangumi"
	backLabel := "返回管理后台"
	if !isAdmin {
		backHref = "/account"
		backLabel = "返回账号设置"
	}

	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>%s · MangaAlpha</title>
<style>
  :root { --bg:#f8fafc; --card:#ffffff; --fg:#0f172a; --muted:#475569; --border:#e2e8f0; }
  @media (prefers-color-scheme: dark) {
    :root { --bg:#0f172a; --card:#1e293b; --fg:#f1f5f9; --muted:#94a3b8; --border:#334155; }
  }
  body { margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center; padding:24px; background:var(--bg); color:var(--fg); font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif; }
  .box { max-width:460px; width:100%%; background:var(--card); border:1px solid var(--border); border-radius:14px; padding:28px 24px; text-align:center; box-shadow:0 12px 32px rgba(0,0,0,.12); }
  .ico { font-size:36px; line-height:1; margin-bottom:10px; }
  h1 { margin:0 0 10px; font-size:20px; color:%s; }
  p { margin:0 0 20px; font-size:14px; line-height:1.65; color:var(--muted); word-break:break-word; }
  .btn { display:inline-block; padding:9px 18px; border-radius:8px; background:#f09199; color:#fff; text-decoration:none; font-size:14px; font-weight:600; border:none; cursor:pointer; }
</style>
</head>
<body>
  <div class="box">
    <div class="ico">%s</div>
    <h1>%s</h1>
    <p>%s</p>
    <a class="btn" href="%s" id="back-btn">%s</a>
  </div>
  <script>
    (function(){
      var payload = %s;
      var targetOrigin = %s;
      try {
        if (window.opener && targetOrigin) {
          window.opener.postMessage(payload, targetOrigin);
          if (payload.ok) {
            setTimeout(function(){ window.close(); }, 900);
          }
        }
      } catch (_) {}
    })();
  </script>
</body>
</html>`,
		html.EscapeString(title),
		accent,
		icon,
		html.EscapeString(title),
		html.EscapeString(message),
		backHref,
		html.EscapeString(backLabel),
		string(payloadJSON),
		string(originJSON),
	)
}
