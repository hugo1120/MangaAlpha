package httpserver

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	userscriptKeyNamePrefix = "Bangumi 油猴脚本"
	userscriptTicketTTL     = 60 * time.Second
	userscriptTicketReads   = 5
	userscriptTicketMaxCap  = 64
)

type userscriptTicket struct {
	origin         string
	apiKey         string
	expiresAt      time.Time
	remainingReads int
}

type userscriptTicketStore struct {
	mu      sync.Mutex
	tickets map[string]*userscriptTicket
	nowFunc func() time.Time
}

func newUserscriptTicketStore() *userscriptTicketStore {
	return &userscriptTicketStore{
		tickets: make(map[string]*userscriptTicket),
		nowFunc: time.Now,
	}
}

func (st *userscriptTicketStore) now() time.Time {
	if st != nil && st.nowFunc != nil {
		return st.nowFunc()
	}
	return time.Now()
}

func (st *userscriptTicketStore) Issue(origin, apiKey string) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	now := st.now()

	st.mu.Lock()
	defer st.mu.Unlock()
	for k, v := range st.tickets {
		if !v.expiresAt.After(now) || v.remainingReads <= 0 {
			delete(st.tickets, k)
		}
	}
	if len(st.tickets) >= userscriptTicketMaxCap {
		st.tickets = make(map[string]*userscriptTicket)
	}
	st.tickets[token] = &userscriptTicket{
		origin:         origin,
		apiKey:         apiKey,
		expiresAt:      now.Add(userscriptTicketTTL),
		remainingReads: userscriptTicketReads,
	}
	return token, nil
}

func (st *userscriptTicketStore) Consume(token string) (origin, apiKey string, ok bool) {
	token = strings.TrimSpace(token)
	if st == nil || token == "" {
		return "", "", false
	}
	now := st.now()
	st.mu.Lock()
	defer st.mu.Unlock()
	item, exists := st.tickets[token]
	if !exists {
		return "", "", false
	}
	if !item.expiresAt.After(now) || item.remainingReads <= 0 {
		delete(st.tickets, token)
		return "", "", false
	}
	item.remainingReads--
	origin, apiKey = item.origin, item.apiKey
	if item.remainingReads <= 0 {
		delete(st.tickets, token)
	}
	return origin, apiKey, true
}

func (s *Server) serveBangumiJump(w http.ResponseWriter, r *http.Request) {
	rawID := strings.TrimSpace(chi.URLParam(r, "subjectID"))
	subjectID, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || subjectID <= 0 {
		s.renderNotFound(w)
		return
	}
	matches, err := s.store.LookupSeriesByBangumiSubject(r.Context(), subjectID)
	if err != nil {
		s.renderWebError(w, err)
		return
	}
	if len(matches) == 1 {
		http.Redirect(w, r, "/series/"+url.PathEscape(matches[0].SeriesID), http.StatusFound)
		return
	}
	if len(matches) > 1 {
		http.Redirect(w, r, "/series/"+url.PathEscape(matches[0].SeriesID)+"?versions=1", http.StatusFound)
		return
	}

	title := strings.TrimSpace(r.URL.Query().Get("title"))
	searchURL := "/libraries"
	if title != "" {
		searchURL = "/libraries?search=" + url.QueryEscape(title)
	}
	bgmURL := fmt.Sprintf("https://bgm.tv/subject/%d", subjectID)
	titleHTML := ""
	if title != "" {
		titleHTML = fmt.Sprintf(`<p style="margin:6px 0 0;color:var(--text-muted,#64748b);font-size:14px">条目标题：%s</p>`, html.EscapeString(title))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>暂未收录 Bangumi #%d · MangaAlpha</title>
<link id="app-favicon" rel="icon" type="image/png" href="/ui/mangaalpha-icon.png">
<link rel="stylesheet" href="/ui/app.css">
<script src="/ui/theme-icon.js"></script>
</head>
<body style="display:grid;place-items:center;min-height:100vh;margin:0;padding:24px">
<main class="card" style="max-width:460px;width:100%%;text-align:center;padding:28px 24px">
  <h1 style="margin:0 0 10px;font-size:20px">本库暂未收录 Bangumi #%d</h1>
  %s
  <p style="margin:12px 0 20px;color:var(--text-muted,#64748b);font-size:13px;line-height:1.6">
    未在当前可见书库中找到绑定该 Bangumi 条目的作品（也可能尚未刮削，或处于隐藏/NSFW 筛选中）。
  </p>
  <div style="display:flex;flex-wrap:wrap;justify-content:center;gap:10px">
    <a class="btn btn-primary" href="%s">去书架搜索</a>
    <a class="btn" href="/">返回书架首页</a>
    <a class="btn" href="%s" target="_blank" rel="noopener noreferrer">返回 Bangumi ↗</a>
  </div>
</main>
</body>
</html>`, subjectID, subjectID, titleHTML, html.EscapeString(searchURL), html.EscapeString(bgmURL))
}

func (s *Server) komgaBangumiLookup(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("subject_id"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("subjectId"))
	}
	subjectID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || subjectID <= 0 {
		writeError(w, http.StatusBadRequest, "INVALID_SUBJECT_ID", "subject_id must be a positive integer.")
		return
	}
	matches, err := s.store.LookupSeriesByBangumiSubject(r.Context(), subjectID)
	if err != nil {
		s.writeDBError(w, r, err)
		return
	}
	jumpURL := fmt.Sprintf("/bgm/%d", subjectID)
	if len(matches) == 1 {
		jumpURL = matches[0].URL
	} else if len(matches) > 1 {
		jumpURL = matches[0].URL + "?versions=1"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subjectId": subjectID,
		"found":     len(matches) > 0,
		"count":     len(matches),
		"jumpUrl":   jumpURL,
		"matches":   matches,
	})
}

func (s *Server) requestOrigin(r *http.Request) string {
	scheme := "http"
	if s.isHTTPSRequest(r) {
		scheme = "https"
	}
	host := strings.TrimSpace(r.Host)
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

func sanitizeConnectHost(origin string) string {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil {
		return ""
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" || strings.ContainsAny(host, " \t\r\n/\\*@") {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return strings.ToLower(host)
}

func escapeJSString(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`'`, `\'`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"<", `\u003c`,
		">", `\u003e`,
	)
	return replacer.Replace(s)
}

func renderUserscriptBody(origin, apiKey string) (string, error) {
	raw, err := staticFiles.ReadFile("static/mangaalpha-bangumi.user.js")
	if err != nil {
		return "", err
	}
	connectLine := ""
	if host := sanitizeConnectHost(origin); host != "" {
		connectLine = "// @connect      " + host
	}
	script := string(raw)
	if connectLine != "" {
		script = strings.ReplaceAll(script, "// __MANGAALPHA_CONNECT_LINE__", connectLine)
	} else {
		script = strings.ReplaceAll(script, "// __MANGAALPHA_CONNECT_LINE__\n", "")
		script = strings.ReplaceAll(script, "// __MANGAALPHA_CONNECT_LINE__", "")
	}
	script = strings.ReplaceAll(script, "__MANGAALPHA_SERVER_URL__", escapeJSString(origin))
	script = strings.ReplaceAll(script, "__MANGAALPHA_API_KEY__", escapeJSString(apiKey))
	return script, nil
}

func writeUserscriptResponse(w http.ResponseWriter, script string) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="mangaalpha-bangumi.user.js"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write([]byte(script))
}

func (s *Server) downloadBangumiUserscript(w http.ResponseWriter, r *http.Request) {
	script, err := renderUserscriptBody(s.requestOrigin(r), "")
	if err != nil {
		http.Error(w, "userscript template unavailable", http.StatusInternalServerError)
		return
	}
	writeUserscriptResponse(w, script)
}

func (s *Server) createUserscriptInstallTicket(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WithKey bool `json:"withKey"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	var (
		rawKey     string
		keyPrefix  string
		rotatedOld int64
	)
	if req.WithKey {
		revoked, err := s.store.DeleteAPIKeysByNamePrefix(r.Context(), userscriptKeyNamePrefix)
		if err != nil {
			s.writeDBError(w, r, err)
			return
		}
		rotatedOld = revoked
		keyName := fmt.Sprintf("%s (%s)", userscriptKeyNamePrefix, time.Now().UTC().Format("2006-01-02"))
		createdKey, info, err := s.store.CreateAPIKey(r.Context(), keyName)
		if err != nil {
			s.writeDBError(w, r, err)
			return
		}
		rawKey = createdKey
		keyPrefix = info.KeyPrefix
	}
	origin := s.requestOrigin(r)
	ticket, err := s.userscriptTickets.Issue(origin, rawKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TICKET_ERROR", "Failed to generate install ticket.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"installUrl":       "/bgm/userscript/" + ticket + "/mangaalpha-bangumi.user.js",
		"expiresInSeconds": int(userscriptTicketTTL.Seconds()),
		"withKey":          req.WithKey,
		"rotatedOldKeys":   rotatedOld,
		"keyPrefix":        keyPrefix,
		"connectHost":      sanitizeConnectHost(origin),
	})
}

func (s *Server) serveUserscriptByTicket(w http.ResponseWriter, r *http.Request) {
	ticket := chi.URLParam(r, "ticket")
	origin, apiKey, ok := s.userscriptTickets.Consume(ticket)
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, "Userscript install link has expired. Please regenerate it from MangaAlpha Admin -> Bangumi.", http.StatusNotFound)
		return
	}
	script, err := renderUserscriptBody(origin, apiKey)
	if err != nil {
		http.Error(w, "userscript template unavailable", http.StatusInternalServerError)
		return
	}
	writeUserscriptResponse(w, script)
}
