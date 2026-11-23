package httpserver

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xJogger/fake-komga-115/internal/buildinfo"
)

const (
	agentAuthRequestTTL     = 10 * time.Minute
	agentAuthMaxPending     = 32
	agentAuthRateWindow     = 10 * time.Minute
	agentAuthRateMaxPerIP   = 10
	agentAuthMaxBodyBytes   = 8 << 10
	agentAuthCodeAlphabet   = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	agentAuthPollIntervalS  = 2
	agentAuthStatusPending  = "pending"
	agentAuthStatusApproved = "approved"
	agentAuthStatusConsumed = "consumed"
	agentAuthStatusRejected = "rejected"
)

type agentAuthRequest struct {
	ID               string
	VerificationCode string
	PollToken        string
	ClientName       string
	Purpose          string
	ClientIP         string
	Status           string
	APIKeyPlain      string
	KeyName          string
	KeyPrefix        string
	CreatedAt        time.Time
	ExpiresAt        time.Time
}

type agentAuthStore struct {
	mu       sync.Mutex
	requests map[string]*agentAuthRequest
	ipHits   map[string][]time.Time
}

func newAgentAuthStore() *agentAuthStore {
	return &agentAuthStore{
		requests: make(map[string]*agentAuthRequest),
		ipHits:   make(map[string][]time.Time),
	}
}

func (st *agentAuthStore) allowCreate(clientIP string, now time.Time) (bool, time.Duration) {
	cutoff := now.Add(-agentAuthRateWindow)
	if len(st.ipHits) > 2048 {
		for ip, hits := range st.ipHits {
			if len(hits) == 0 || hits[len(hits)-1].Before(cutoff) {
				delete(st.ipHits, ip)
			}
		}
	}
	hits := st.ipHits[clientIP]
	kept := hits[:0]
	for _, ts := range hits {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= agentAuthRateMaxPerIP {
		st.ipHits[clientIP] = kept
		retryAfter := kept[0].Add(agentAuthRateWindow).Sub(now)
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		return false, retryAfter
	}
	st.ipHits[clientIP] = append(kept, now)
	return true, 0
}

func (st *agentAuthStore) pruneLocked(now time.Time) {
	for id, req := range st.requests {
		if now.After(req.ExpiresAt) {
			delete(st.requests, id)
		}
	}
}

func generateVerificationCode() (string, error) {
	var b strings.Builder
	maxIdx := big.NewInt(int64(len(agentAuthCodeAlphabet)))
	for i := 0; i < 6; i++ {
		if i == 3 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, maxIdx)
		if err != nil {
			return "", err
		}
		b.WriteByte(agentAuthCodeAlphabet[n.Int64()])
	}
	return b.String(), nil
}

func randomHexBytes(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func (st *agentAuthStore) Create(clientName, purpose, clientIP string) (*agentAuthRequest, time.Duration, error) {
	now := time.Now().UTC()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked(now)

	if ok, retryAfter := st.allowCreate(clientIP, now); !ok {
		return nil, retryAfter, errors.New("rate limited")
	}
	if len(st.requests) >= agentAuthMaxPending {
		return nil, 30 * time.Second, errors.New("too many pending authorization requests")
	}

	reqID, err := randomHexBytes(16)
	if err != nil {
		return nil, 0, err
	}
	pollToken, err := randomHexBytes(32)
	if err != nil {
		return nil, 0, err
	}
	code, err := generateVerificationCode()
	if err != nil {
		return nil, 0, err
	}

	req := &agentAuthRequest{
		ID:               reqID,
		VerificationCode: code,
		PollToken:        pollToken,
		ClientName:       clientName,
		Purpose:          purpose,
		ClientIP:         clientIP,
		Status:           agentAuthStatusPending,
		CreatedAt:        now,
		ExpiresAt:        now.Add(agentAuthRequestTTL),
	}
	st.requests[reqID] = req
	copied := *req
	return &copied, 0, nil
}

func (st *agentAuthStore) GetForAdmin(id string) (*agentAuthRequest, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, false
	}
	now := time.Now().UTC()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked(now)
	req, ok := st.requests[id]
	if !ok {
		return nil, false
	}
	copied := *req
	copied.PollToken = ""
	copied.APIKeyPlain = ""
	return &copied, true
}

func (st *agentAuthStore) Approve(id, keyName, keyPrefix, rawKey string) (*agentAuthRequest, error) {
	id = strings.TrimSpace(id)
	now := time.Now().UTC()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked(now)
	req, ok := st.requests[id]
	if !ok {
		return nil, errors.New("authorization request not found or expired")
	}
	if req.Status != agentAuthStatusPending {
		return nil, fmt.Errorf("authorization request is already %s", req.Status)
	}
	req.Status = agentAuthStatusApproved
	req.KeyName = keyName
	req.KeyPrefix = keyPrefix
	req.APIKeyPlain = rawKey
	copied := *req
	copied.PollToken = ""
	return &copied, nil
}

func (st *agentAuthStore) Reject(id string) (*agentAuthRequest, error) {
	id = strings.TrimSpace(id)
	now := time.Now().UTC()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked(now)
	req, ok := st.requests[id]
	if !ok {
		return nil, errors.New("authorization request not found or expired")
	}
	if req.Status != agentAuthStatusPending {
		return nil, fmt.Errorf("authorization request is already %s", req.Status)
	}
	req.Status = agentAuthStatusRejected
	req.APIKeyPlain = ""
	copied := *req
	copied.PollToken = ""
	return &copied, nil
}

// Poll checks an authorization request using constant-time pollToken validation.
// When status is approved, it returns the plaintext API key ONCE and immediately clears it from memory.
func (st *agentAuthStore) Poll(id, pollToken string) (reqCopy agentAuthRequest, apiKey string, found bool, authorized bool) {
	id = strings.TrimSpace(id)
	pollToken = strings.TrimSpace(pollToken)
	if id == "" || pollToken == "" {
		return agentAuthRequest{}, "", false, false
	}
	now := time.Now().UTC()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked(now)
	req, ok := st.requests[id]
	if !ok {
		return agentAuthRequest{}, "", false, false
	}
	if subtle.ConstantTimeCompare([]byte(req.PollToken), []byte(pollToken)) != 1 {
		return agentAuthRequest{}, "", true, false
	}
	copied := *req
	copied.PollToken = ""
	if req.Status == agentAuthStatusApproved && req.APIKeyPlain != "" {
		apiKey = req.APIKeyPlain
		req.APIKeyPlain = ""
		req.Status = agentAuthStatusConsumed
		copied.Status = agentAuthStatusApproved
		copied.APIKeyPlain = ""
		return copied, apiKey, true, true
	}
	copied.APIKeyPlain = ""
	return copied, "", true, true
}

func isPublicAgentDiscoveryPath(p, method string) bool {
	if method == http.MethodGet || method == http.MethodHead {
		if p == "/api/agent/v1/guide" || p == "/api/agent/v1/playbooks/bangumi-match" {
			return true
		}
	}
	return false
}

func isPublicAgentAuthFlowPath(p, method string) bool {
	if method == http.MethodPost && p == "/api/agent/v1/auth/requests" {
		return true
	}
	if method == http.MethodGet && strings.HasPrefix(p, "/api/agent/v1/auth/requests/") {
		rest := strings.TrimPrefix(p, "/api/agent/v1/auth/requests/")
		return rest != "" && !strings.Contains(rest, "/")
	}
	return false
}

func sanitizeShortLabel(s string, maxRunes int, fallback string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return fallback
	}
	runes := []rune(s)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return s
}

func (s *Server) buildAgentGuideMarkdown(origin string) string {
	if origin == "" {
		origin = "<MANGAALPHA_BASE_URL>"
	}
	manualKeyURL := origin + "/admin?tab=security&presetKey=" + url.QueryEscape("AI Agent (Bangumi 匹配)")
	raw := `# MangaAlpha AI Agent 接入与 Bangumi 匹配指引

> 服务端版本：{{VERSION}} · API 基址：{{BT}}{{ORIGIN}}/api/agent/v1{{BT}}

本指引不含任何私密书库数据，可安全公开读取。完成第 1 步获得授权后，再按第 2 步执行未匹配条目的 Bangumi 精确匹配。

---

## 第 1 步：向用户申请授权（一键确认，无需手输密码）

如果你**尚未持有**有效的 MangaAlpha API Key，请按以下流程引导用户一键授权：

1. **发起授权申请**（无需鉴权）：
{{FENCE}}bash
curl -fsS -X POST "{{ORIGIN}}/api/agent/v1/auth/requests" \
  -H "Content-Type: application/json" \
  -d '{"clientName":"你的 Agent 名称","purpose":"匹配未命中的 Bangumi 漫画条目"}'
{{FENCE}}
   接口会返回 {{BT}}requestId{{BT}}、{{BT}}verificationCode{{BT}}（6 位核对码）、{{BT}}pollToken{{BT}}、{{BT}}approvalUrl{{BT}}（直达一键授权页）与 {{BT}}manualKeyUrl{{BT}}（备用手动建 Key 页），有效期 10 分钟。

2. **将直达链接与核对码发给用户**：
   - 请用户点击 **{{BT}}approvalUrl{{BT}}**（若浏览器未登录，会自动先跳转登录页再回到授权确认页）；
   - 请用户核对页面上的 **核对码** 与你发出的 {{BT}}verificationCode{{BT}} 完全一致后，点击 **「批准授权」**；
   - 若用户环境不方便轮询，也可点击备用链接：{{BT}}{{MANUAL_KEY_URL}}{{BT}} 手动点一下生成 API Key 并粘贴给你。

3. **轮询领取 API Key**（每 2~3 秒一次，直到 {{BT}}status{{BT}} 变为 {{BT}}approved{{BT}}、{{BT}}rejected{{BT}} 或过期）：
{{FENCE}}bash
curl -fsS -H "X-Poll-Token: <pollToken>" \
  "{{ORIGIN}}/api/agent/v1/auth/requests/<requestId>"
{{FENCE}}
   - 当返回 {{BT}}"status":"approved"{{BT}} 时，响应体中的 {{BT}}apiKey{{BT}} 即为新签发的专属密钥（**仅下发一次，服务端随后立即销毁内存明文**，请妥善保存用于本次会话请求头 {{BT}}Authorization: Bearer <apiKey>{{BT}}）。
   - 用户随时可在「管理后台 → 账号与 API Key」查看并吊销该密钥。

---

## 第 2 步：未匹配条目 Bangumi 精确匹配 SOP

获得 {{BT}}API_KEY{{BT}} 后，请严格遵守以下防幻觉流程操作：

### 2.1 了解概况与拉取未匹配列表
1. 先查看全局概览与能力清单：
   - {{BT}}GET {{ORIGIN}}/api/agent/v1/overview{{BT}}
   - {{BT}}GET {{ORIGIN}}/api/agent/v1/manifest{{BT}}
2. 分页拉取规则未命中的作品（**默认排除 NSFW**）：
   - {{BT}}GET {{ORIGIN}}/api/agent/v1/series?scrape_status=unmatched&nsfw=exclude&page=0&size=50{{BT}}
   - 若用户也希望处理尚未刮削的作品，可再拉取 {{BT}}scrape_status=none&nsfw=exclude{{BT}}。
   - **NSFW 策略**：**默认必须携带 {{BT}}nsfw=exclude{{BT}}**；只有当用户在对话中明确要求包含 NSFW 时，才改为 {{BT}}nsfw=all{{BT}} 或 {{BT}}nsfw=only{{BT}}。

### 2.2 逐条核实候选（严禁编造 Subject ID！）
对每一部未匹配作品：
1. 优先分析返回项中的 {{BT}}folderName{{BT}}、{{BT}}relativePath{{BT}}（上级目录常含作者/分类）、{{BT}}booksCount{{BT}}、{{BT}}oneShot{{BT}}。
2. 如文件夹名信息不足，调用 {{BT}}GET {{ORIGIN}}/api/agent/v1/series/{seriesID}{{BT}} 查看单册文件名列表获取更多线索。
3. 调用 {{BT}}GET {{ORIGIN}}/api/agent/v1/series/{seriesID}/bangumi/candidates{{BT}} 获取默认候选。
4. 若默认候选为空或不理想，请由你清洗目录名中的噪声标签（汉化组、卷数、清晰度、括号标记），或推导出**日文原名 / 港台常见译名 / 作者名**，然后带 {{BT}}?q=<关键词>{{BT}} 再次调用：
   - {{BT}}GET {{ORIGIN}}/api/agent/v1/series/{seriesID}/bangumi/candidates?q=<清洗或翻译后的关键词>{{BT}}
5. **硬性防幻觉约束**：
   - **绝对禁止凭记忆盲猜或编造数字 {{BT}}subjectId{{BT}}**！
   - 你提交绑定的每一个 {{BT}}subjectId{{BT}}，都**必须**出现在 {{BT}}/bangumi/candidates{{BT}} 接口的真实返回结果中（或通过 {{BT}}?q=<subjectId>{{BT}} 查回真实条目核对过 {{BT}}name{{BT}} / {{BT}}nameCn{{BT}} / {{BT}}authors{{BT}} / {{BT}}platformLabel{{BT}}）。
   - 优先选择平台类型为 **「漫画」** 的本篇单行本/连载条目（除非路径明显标明为轻小说/画集/公式书）。
   - **宁缺毋滥**：如果候选存在多版本冲突、同名歧义或无法确认，**请跳过该条目并在最终报告中列出，交由用户人工确认**。

### 2.3 批量提交绑定与结果汇报
1. 每攒够 10~50 条已核实的绑定，统一调用批量接口（单次上限 200 条）：
{{FENCE}}bash
curl -fsS -X POST "{{ORIGIN}}/api/agent/v1/series/batch" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "operations": [
      {"seriesId": "<seriesID>", "action": "bind_bangumi", "subjectId": 123456}
    ]
  }'
{{FENCE}}
2. 全部完成后，向用户输出一份清晰汇总表：
   - 本次检查总数、成功绑定数、跳过/待确认数；
   - 已绑定清单（原目录名 → Bangumi #subjectId + 标题）；
   - 跳过清单及原因（如：同人志未收录、同名歧义、信息不足）。
`
	replacer := strings.NewReplacer(
		"{{VERSION}}", buildinfo.Version,
		"{{ORIGIN}}", origin,
		"{{MANUAL_KEY_URL}}", manualKeyURL,
		"{{FENCE}}", "```",
		"{{BT}}", "`",
	)
	return replacer.Replace(raw)
}

func (s *Server) agentGuide(w http.ResponseWriter, r *http.Request) {
	origin := s.requestOrigin(r)
	md := s.buildAgentGuideMarkdown(origin)
	wantJSON := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "json") ||
		(strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") &&
			!strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/markdown"))

	w.Header().Set("Cache-Control", "no-store")
	if wantJSON {
		manualKeyURL := ""
		if origin != "" {
			manualKeyURL = origin + "/admin?tab=security&presetKey=" + url.QueryEscape("AI Agent (Bangumi 匹配)")
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"name":                 "MangaAlpha Agent Guide",
			"appVersion":           buildinfo.Version,
			"basePath":             "/api/agent/v1",
			"authRequestEndpoint":  origin + "/api/agent/v1/auth/requests",
			"manualKeyUrl":         manualKeyURL,
			"manifestEndpoint":     origin + "/api/agent/v1/manifest",
			"bangumiMatchPlaybook": origin + "/api/agent/v1/playbooks/bangumi-match",
			"defaultNsfwFilter":    "exclude",
			"guideMarkdown":        md,
		})
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(md))
}

func (s *Server) createAgentAuthRequest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, agentAuthMaxBodyBytes)
	var req struct {
		ClientName string `json:"clientName"`
		Purpose    string `json:"purpose"`
	}
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &req) {
			return
		}
	}
	clientName := sanitizeShortLabel(req.ClientName, 60, "AI Agent")
	purpose := sanitizeShortLabel(req.Purpose, 180, "访问 MangaAlpha Agent API 并协助整理书库")
	clientIP := s.trustedProxies.clientIP(r)

	created, retryAfter, err := s.agentAuth.Create(clientName, purpose, clientIP)
	if err != nil {
		if retryAfter > 0 {
			writeRateLimited(w, retryAfter)
			return
		}
		writeError(w, http.StatusInternalServerError, "AGENT_AUTH_CREATE_FAILED", "Failed to create authorization request.")
		return
	}

	origin := s.requestOrigin(r)
	approvalPath := "/agent-auth/" + created.ID
	approvalURL := approvalPath
	pollURL := "/api/agent/v1/auth/requests/" + created.ID
	manualKeyURL := "/admin?tab=security&presetKey=" + url.QueryEscape("AI Agent · "+created.ClientName)
	if origin != "" {
		approvalURL = origin + approvalPath
		pollURL = origin + pollURL
		manualKeyURL = origin + manualKeyURL
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{
		"requestId":           created.ID,
		"verificationCode":    created.VerificationCode,
		"pollToken":           created.PollToken,
		"approvalUrl":         approvalURL,
		"manualKeyUrl":        manualKeyURL,
		"pollUrl":             pollURL,
		"pollIntervalSeconds": agentAuthPollIntervalS,
		"expiresAt":           created.ExpiresAt.Format(time.RFC3339),
		"expiresInSeconds":    int(agentAuthRequestTTL.Seconds()),
		"instruction":         "请向用户发送 approvalUrl 和 verificationCode，待用户在浏览器中点击批准后，携带 X-Poll-Token 请求 pollUrl 领取一次性下发的 API Key。",
	})
}

func (s *Server) pollAgentAuthRequest(w http.ResponseWriter, r *http.Request) {
	reqID := chi.URLParam(r, "requestID")
	pollToken := strings.TrimSpace(r.Header.Get("X-Poll-Token"))
	if pollToken == "" {
		if auth := strings.TrimSpace(r.Header.Get("Authorization")); len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
			pollToken = strings.TrimSpace(auth[7:])
		}
	}
	if pollToken == "" {
		writeError(w, http.StatusUnauthorized, "MISSING_POLL_TOKEN", "X-Poll-Token header is required.")
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	item, apiKey, found, authorized := s.agentAuth.Poll(reqID, pollToken)
	if !found {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Authorization request not found or expired.")
		return
	}
	if !authorized {
		writeError(w, http.StatusForbidden, "INVALID_POLL_TOKEN", "Invalid poll token.")
		return
	}

	resp := map[string]any{
		"requestId": item.ID,
		"status":    item.Status,
		"expiresAt": item.ExpiresAt.Format(time.RFC3339),
	}
	if item.Status == agentAuthStatusApproved && apiKey != "" {
		origin := s.requestOrigin(r)
		resp["apiKey"] = apiKey
		resp["keyName"] = item.KeyName
		resp["keyPrefix"] = item.KeyPrefix
		resp["header"] = "Authorization: Bearer " + apiKey
		resp["manifestUrl"] = origin + "/api/agent/v1/manifest"
		resp["playbookUrl"] = origin + "/api/agent/v1/playbooks/bangumi-match"
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getAdminAgentAuthRequest(w http.ResponseWriter, r *http.Request) {
	reqID := chi.URLParam(r, "requestID")
	item, ok := s.agentAuth.GetForAdmin(reqID)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "授权请求不存在或已过期，请让 AI Agent 重新发起申请。")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"requestId":        item.ID,
		"verificationCode": item.VerificationCode,
		"clientName":       item.ClientName,
		"purpose":          item.Purpose,
		"clientIp":         item.ClientIP,
		"status":           item.Status,
		"keyName":          item.KeyName,
		"keyPrefix":        item.KeyPrefix,
		"createdAt":        item.CreatedAt.Format(time.RFC3339),
		"expiresAt":        item.ExpiresAt.Format(time.RFC3339),
	})
}

func (s *Server) approveAdminAgentAuthRequest(w http.ResponseWriter, r *http.Request) {
	reqID := chi.URLParam(r, "requestID")
	existing, ok := s.agentAuth.GetForAdmin(reqID)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "授权请求不存在或已过期。")
		return
	}
	if existing.Status != agentAuthStatusPending {
		writeError(w, http.StatusConflict, "ALREADY_HANDLED", "该授权请求已被处理。")
		return
	}
	var body struct {
		KeyName string `json:"keyName"`
	}
	if r.ContentLength != 0 {
		if !decodeJSON(w, r, &body) {
			return
		}
	}
	principal, _ := s.currentPrincipal(r)
	keyName := sanitizeShortLabel(body.KeyName, 80, "AI Agent · "+existing.ClientName)
	rawKey, meta, err := s.store.CreateAPIKeyForUser(r.Context(), principal.UserID, keyName)
	if err != nil {
		s.writeDBError(w, r, err)
		return
	}
	approved, err := s.agentAuth.Approve(reqID, meta.Name, meta.KeyPrefix, rawKey)
	if err != nil {
		// Race: rollback the newly created API key if request was simultaneously handled/expired.
		_ = s.store.DeleteAPIKeyForUser(r.Context(), principal.UserID, meta.ID)
		writeError(w, http.StatusConflict, "APPROVE_CONFLICT", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"requestId": approved.ID,
		"status":    approved.Status,
		"keyId":     meta.ID,
		"keyName":   meta.Name,
		"keyPrefix": meta.KeyPrefix,
		"key":       rawKey,
	})
}

func (s *Server) rejectAdminAgentAuthRequest(w http.ResponseWriter, r *http.Request) {
	reqID := chi.URLParam(r, "requestID")
	rejected, err := s.agentAuth.Reject(reqID)
	if err != nil {
		writeError(w, http.StatusConflict, "REJECT_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"requestId": rejected.ID,
		"status":    rejected.Status,
	})
}
