package httpserver

import (
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	userscriptKeyNamePrefix = "Bangumi 油猴脚本"
	userscriptLatestPath    = "/bgm/userscript/latest.user.js"
	userscriptPairPath      = "/bgm/userscript/pair"
)

func isPublicUserscriptPath(p, method string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	return p == userscriptLatestPath
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
	resp := map[string]any{
		"subjectId": subjectID,
		"found":     len(matches) > 0,
		"count":     len(matches),
		"jumpUrl":   jumpURL,
		"matches":   matches,
	}
	if s.bangumi != nil {
		if summary, src, ok := s.bangumi.ArchiveSummaryByID(r.Context(), subjectID); ok {
			resp["summary"] = summary
			resp["summarySource"] = src
		}
	}
	writeJSON(w, http.StatusOK, resp)
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

func replaceOptionalMetaLine(script, placeholder, line string) string {
	if line != "" {
		return strings.ReplaceAll(script, placeholder, line)
	}
	script = strings.ReplaceAll(script, placeholder+"\n", "")
	return strings.ReplaceAll(script, placeholder, "")
}

// renderUserscriptBody renders the strictly keyless Tampermonkey/Violentmonkey userscript.
// API keys are never embedded in the script source; instead they are paired into GM_setValue
// via /bgm/userscript/pair so script updates and diffs stay 100% clean and key-safe.
func renderUserscriptBody(origin string) (string, error) {
	raw, err := staticFiles.ReadFile("static/mangaalpha-bangumi.user.js")
	if err != nil {
		return "", err
	}
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	host := sanitizeConnectHost(origin)

	connectLine := ""
	pairMatchLine := ""
	if host != "" {
		connectLine = "// @connect      " + host
		matchHost := host
		if strings.Contains(matchHost, ":") {
			matchHost = "[" + matchHost + "]"
		}
		pairMatchLine = "// @match        *://" + matchHost + userscriptPairPath + "*"
	}
	updateLine := ""
	downloadLine := ""
	if origin != "" {
		latestURL := origin + userscriptLatestPath
		updateLine = "// @updateURL    " + latestURL
		downloadLine = "// @downloadURL  " + latestURL
	}

	script := string(raw)
	script = replaceOptionalMetaLine(script, "// __MANGAALPHA_PAIR_MATCH_LINE__", pairMatchLine)
	script = replaceOptionalMetaLine(script, "// __MANGAALPHA_UPDATE_URL_LINE__", updateLine)
	script = replaceOptionalMetaLine(script, "// __MANGAALPHA_DOWNLOAD_URL_LINE__", downloadLine)
	script = replaceOptionalMetaLine(script, "// __MANGAALPHA_CONNECT_LINE__", connectLine)
	script = strings.ReplaceAll(script, "__MANGAALPHA_SERVER_URL__", escapeJSString(origin))
	return script, nil
}

func writeUserscriptResponse(w http.ResponseWriter, script string) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="mangaalpha-bangumi.user.js"`)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = w.Write([]byte(script))
}

func (s *Server) serveLatestUserscript(w http.ResponseWriter, r *http.Request) {
	script, err := renderUserscriptBody(s.requestOrigin(r))
	if err != nil {
		http.Error(w, "userscript template unavailable", http.StatusInternalServerError)
		return
	}
	writeUserscriptResponse(w, script)
}

func (s *Server) downloadBangumiUserscript(w http.ResponseWriter, r *http.Request) {
	s.serveLatestUserscript(w, r)
}

func (s *Server) createUserscriptPairKey(w http.ResponseWriter, r *http.Request) {
	principal, _ := s.currentPrincipal(r)
	revoked, err := s.store.DeleteAPIKeysByNamePrefixForUser(r.Context(), principal.UserID, userscriptKeyNamePrefix)
	if err != nil {
		s.writeDBError(w, r, err)
		return
	}
	keyName := fmt.Sprintf("%s (%s)", userscriptKeyNamePrefix, time.Now().UTC().Format("2006-01-02"))
	createdKey, info, err := s.store.CreateAPIKeyForUser(r.Context(), principal.UserID, keyName)
	if err != nil {
		s.writeDBError(w, r, err)
		return
	}
	origin := s.requestOrigin(r)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":            createdKey,
		"keyPrefix":      info.KeyPrefix,
		"rotatedOldKeys": revoked,
		"pairPath":       userscriptPairPath,
		"installUrl":     userscriptLatestPath,
		"connectHost":    sanitizeConnectHost(origin),
	})
}

func (s *Server) serveUserscriptPairPage(w http.ResponseWriter, r *http.Request) {
	backHref := "/admin?tab=bangumi"
	backLabel := "返回 Bangumi 刮削中心"
	if principal, ok := s.currentPrincipal(r); ok && !principal.IsAdmin {
		backHref = "/account"
		backLabel = "返回账号设置"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="dark light">
<title>同步专属 Key 到油猴脚本 · MangaAlpha</title>
<link id="app-favicon" rel="icon" type="image/png" href="/ui/mangaalpha-icon.png">
<link rel="stylesheet" href="/ui/app.css">
<script src="/ui/theme-icon.js"></script>
</head>
<body style="display:grid;place-items:center;min-height:100vh;margin:0;padding:24px">
<main id="ma-pair-card" class="card" style="max-width:520px;width:100%%;padding:26px 24px">
  <h1 style="margin:0 0 10px;font-size:20px">同步专属 Key 到油猴脚本</h1>
  <p id="ma-pair-status" style="margin:0 0 10px;font-size:14px;font-weight:600;color:var(--accent)">正在等待油猴脚本自动写入本地存储…</p>
  <p id="ma-pair-detail" style="margin:0 0 16px;font-size:13px;line-height:1.65;color:var(--text-secondary)">
    如果你已经在当前浏览器安装并启用了新版 MangaAlpha 油猴脚本，本页面会在 1 秒内自动完成写入并清空地址栏中的密钥锚点。
  </p>
  <div id="ma-pair-fallback" style="display:none;padding:12px 14px;border-radius:10px;background:var(--bg-input);border:1px solid var(--border);margin-bottom:16px">
    <div style="font-size:12.5px;font-weight:600;color:var(--warn);margin-bottom:8px">未检测到新版油猴脚本自动接管（可能尚未安装、未升级至 2.7.2+ 或当前扩展已禁用）</div>
    <div style="display:flex;flex-wrap:wrap;gap:8px;margin-bottom:10px">
      <a class="btn btn-primary btn-sm" href="%s" target="_blank" rel="noopener noreferrer">先安装 / 更新油猴脚本 ↗</a>
    </div>
    <div style="font-size:12px;color:var(--text-muted);margin-bottom:4px">也可以在油猴面板点击「⚙ 配置」手动粘贴下方密钥（离开页面后不再显示）：</div>
    <div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">
      <code id="ma-pair-key-box" class="code-box" style="flex:1;min-width:200px;margin:0;user-select:all"></code>
      <button type="button" class="btn btn-sm" id="ma-pair-copy-btn">复制密钥</button>
    </div>
  </div>
  <div style="display:flex;flex-wrap:wrap;gap:10px">
    <a class="btn btn-primary btn-sm" href="%s">%s</a>
    <a class="btn btn-sm" href="https://bgm.tv/" target="_blank" rel="noopener noreferrer">打开 Bangumi ↗</a>
  </div>
</main>
<script>
  (function(){
    var rawHash = (location.hash || '').replace(/^#/, '');
    var params = new URLSearchParams(rawHash);
    var key = (params.get('key') || '').trim();
    var copyBtn = document.getElementById('ma-pair-copy-btn');
    var keyBox = document.getElementById('ma-pair-key-box');
    if (keyBox && key) keyBox.textContent = key;
    if (copyBtn && key) {
      copyBtn.onclick = function(){
        if (navigator.clipboard && window.isSecureContext) {
          navigator.clipboard.writeText(key).then(function(){ copyBtn.textContent = '已复制'; }).catch(function(){});
          return;
        }
        var ta = document.createElement('textarea');
        ta.value = key;
        ta.style.position = 'fixed';
        ta.style.left = '-9999px';
        document.body.appendChild(ta);
        ta.select();
        try { document.execCommand('copy'); copyBtn.textContent = '已复制'; } catch (_) {}
        document.body.removeChild(ta);
      };
    }
    setTimeout(function(){
      if (!document.documentElement.dataset.mangaalphaUserscriptVersion) {
        var st = document.getElementById('ma-pair-status');
        var fb = document.getElementById('ma-pair-fallback');
        if (st) {
          st.textContent = '⚠️ 未检测到已启用的新版油猴脚本';
          st.style.color = 'var(--warn)';
        }
        if (fb && key) fb.style.display = '';
      }
    }, 1200);
  })();
</script>
</body>
</html>`, userscriptLatestPath, backHref, html.EscapeString(backLabel))
}
