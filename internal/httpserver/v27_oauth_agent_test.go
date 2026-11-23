package httpserver

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xJogger/fake-komga-115/internal/bangumi"
	"github.com/xJogger/fake-komga-115/internal/database"
)

func claimAndLoginCookie(t *testing.T, store *database.Store) string {
	t.Helper()
	ctx := context.Background()
	if _, err := store.ClaimAdmin(ctx, "owner", "SuperStrongPass123!"); err != nil {
		t.Fatalf("claim admin: %v", err)
	}
	tok, _, err := store.CreateSession(ctx, "owner", true, "test-agent")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return sessionCookieName + "=" + tok
}

func TestV27FooterTextIsMangaAlpha(t *testing.T) {
	_, store, _, ts := setupV27TestServer(t, "")
	cookie := claimAndLoginCookie(t, store)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Header.Set("Cookie", cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	html := string(body)
	if !strings.Contains(html, ">MangaAlpha</a>") {
		t.Fatalf("expected footer link text MangaAlpha, got: %s", html)
	}
	if strings.Contains(html, "GitHub 源码") {
		t.Fatalf("unexpected legacy footer text GitHub 源码 in app.html")
	}
}

func TestV27BangumiOAuthFlowAndSecretRedaction(t *testing.T) {
	_, store, bgmSvc, ts := setupV27TestServer(t, "")
	cookie := claimAndLoginCookie(t, store)

	mockBgm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/oauth/access_token":
			_ = r.ParseForm()
			switch r.FormValue("grant_type") {
			case "authorization_code":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  "live_oauth_access_1",
					"refresh_token": "live_oauth_refresh_1",
					"expires_in":    604800,
					"token_type":    "Bearer",
					"user_id":       777,
				})
			case "refresh_token":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token":  "live_oauth_access_2",
					"refresh_token": "live_oauth_refresh_2",
					"expires_in":    604800,
					"token_type":    "Bearer",
				})
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v0/me":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       777,
				"username": "bgmoauthuser",
				"nickname": "漫友OAuth",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockBgm.Close()
	bgmSvc.SetAPIBaseURLForTest(mockBgm.URL)
	bgmSvc.SetOAuthBaseURLForTest(mockBgm.URL)

	// 1. Admin starts OAuth flow
	startReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/admin/api/bangumi/tracker/oauth/start", nil)
	startReq.Header.Set("Cookie", cookie)
	startResp, err := http.DefaultClient.Do(startReq)
	if err != nil {
		t.Fatalf("oauth start: %v", err)
	}
	defer startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on oauth start, got %d", startResp.StatusCode)
	}
	var startBody struct {
		AuthorizeURL string `json:"authorizeUrl"`
		CallbackURL  string `json:"callbackUrl"`
		State        string `json:"state"`
	}
	_ = json.NewDecoder(startResp.Body).Decode(&startBody)
	if startBody.State == "" || !strings.Contains(startBody.AuthorizeURL, "client_id="+bangumi.DefaultOAuthClientID) {
		t.Fatalf("unexpected oauth start response: %+v", startBody)
	}

	// 2. Callback works WITHOUT session cookie (state-authenticated, one-time)
	cbURL := ts.URL + bangumiOAuthCallbackPath + "?code=auth_code_ok&state=" + url.QueryEscape(startBody.State)
	cbResp, err := http.Get(cbURL)
	if err != nil {
		t.Fatalf("oauth callback: %v", err)
	}
	cbBytes, _ := io.ReadAll(cbResp.Body)
	cbResp.Body.Close()
	if cbResp.StatusCode != http.StatusOK || !strings.Contains(string(cbBytes), "MANGAALPHA_BGM_OAUTH_DONE") {
		t.Fatalf("expected 200 callback HTML with postMessage, got %d: %s", cbResp.StatusCode, string(cbBytes))
	}

	// 3. Replay of same state MUST be rejected (one-time consume)
	replayResp, err := http.Get(cbURL)
	if err != nil {
		t.Fatalf("replay callback: %v", err)
	}
	replayResp.Body.Close()
	if replayResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on replayed oauth state, got %d", replayResp.StatusCode)
	}

	// 4. Manual refresh endpoint rotates token cleanly
	refReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/admin/api/bangumi/tracker/oauth/refresh", nil)
	refReq.Header.Set("Cookie", cookie)
	refResp, err := http.DefaultClient.Do(refReq)
	if err != nil {
		t.Fatalf("oauth refresh: %v", err)
	}
	defer refResp.Body.Close()
	if refResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on oauth refresh, got %d", refResp.StatusCode)
	}

	// 5. Save custom OAuth secret and confirm /admin/api/settings NEVER leaks any token/secret!
	cfgPayload := `{"customOAuthClientId":"bgm_custom_99","customOAuthClientSecret":"super_secret_oauth_xyz"}`
	cfgReq, _ := http.NewRequest(http.MethodPut, ts.URL+"/admin/api/bangumi/tracker/config", strings.NewReader(cfgPayload))
	cfgReq.Header.Set("Cookie", cookie)
	cfgReq.Header.Set("Content-Type", "application/json")
	cfgResp, err := http.DefaultClient.Do(cfgReq)
	if err != nil {
		t.Fatalf("save custom oauth: %v", err)
	}
	cfgResp.Body.Close()
	if cfgResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 saving custom oauth app, got %d", cfgResp.StatusCode)
	}

	setReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/api/settings", nil)
	setReq.Header.Set("Cookie", cookie)
	setResp, err := http.DefaultClient.Do(setReq)
	if err != nil {
		t.Fatalf("get settings: %v", err)
	}
	setBytes, _ := io.ReadAll(setResp.Body)
	setResp.Body.Close()
	for _, secret := range []string{"live_oauth_access", "live_oauth_refresh", "super_secret_oauth_xyz"} {
		if strings.Contains(string(setBytes), secret) {
			t.Fatalf("settings endpoint leaked secret %q: %s", secret, string(setBytes))
		}
	}

	// 6. Manual fallback exchange endpoint accepts pasted callback URL
	exReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/admin/api/bangumi/tracker/oauth/exchange",
		strings.NewReader(`{"codeOrUrl":"http://example.local/admin/api/bangumi/tracker/oauth/callback?code=manual_code_1&state=any"}`))
	exReq.Header.Set("Cookie", cookie)
	exReq.Header.Set("Content-Type", "application/json")
	exResp, err := http.DefaultClient.Do(exReq)
	if err != nil {
		t.Fatalf("manual exchange: %v", err)
	}
	exResp.Body.Close()
	if exResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on manual oauth exchange, got %d", exResp.StatusCode)
	}
}

func TestV27AgentGuideAndOneClickAuthFlow(t *testing.T) {
	_, store, _, ts := setupV27TestServer(t, "")
	cookie := claimAndLoginCookie(t, store)
	ctx := context.Background()

	// Seed a private library & series to ensure the public guide NEVER leaks any private data!
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = store.DB().ExecContext(ctx,
		`INSERT INTO libraries(id,name,provider_type,root_cid,root_path,enabled,created_at,updated_at) VALUES('lib_priv','SuperSecretPrivateMangaLib','local','local:/tmp',?,1,?,?)`,
		t.TempDir(), now, now)
	_, _ = store.DB().ExecContext(ctx,
		`INSERT INTO series(id,library_id,cid,name,relative_path,books_count,created_at,updated_at,seen_scan_id) VALUES('s_priv','lib_priv','c_priv','SecretUnpublishedComic','SecretUnpublishedComic',1,?,?,'sc1')`,
		now, now)

	// 1. Public guide works WITHOUT auth, reflects dynamic origin, defaults to nsfw=exclude, leaks ZERO private names
	guideResp, err := http.Get(ts.URL + "/api/agent/v1/guide")
	if err != nil {
		t.Fatalf("GET guide: %v", err)
	}
	guideBytes, _ := io.ReadAll(guideResp.Body)
	guideResp.Body.Close()
	guideText := string(guideBytes)
	if guideResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on public guide, got %d", guideResp.StatusCode)
	}
	if !strings.Contains(guideText, ts.URL+"/api/agent/v1/auth/requests") || !strings.Contains(guideText, "nsfw=exclude") {
		t.Fatalf("guide missing dynamic origin or default nsfw=exclude rule:\n%s", guideText)
	}
	if strings.Contains(guideText, "SuperSecretPrivateMangaLib") || strings.Contains(guideText, "SecretUnpublishedComic") {
		t.Fatalf("public guide leaked private library/series names!")
	}

	// JSON format also works
	jsonGuideResp, err := http.Get(ts.URL + "/api/agent/v1/playbooks/bangumi-match?format=json")
	if err != nil {
		t.Fatalf("GET json playbook: %v", err)
	}
	var jsonGuide map[string]any
	_ = json.NewDecoder(jsonGuideResp.Body).Decode(&jsonGuide)
	jsonGuideResp.Body.Close()
	if jsonGuideResp.StatusCode != http.StatusOK || jsonGuide["defaultNsfwFilter"] != "exclude" {
		t.Fatalf("unexpected json playbook response: %d %+v", jsonGuideResp.StatusCode, jsonGuide)
	}

	// 2. Agent creates an authorization request without auth
	createResp, err := http.Post(
		ts.URL+"/api/agent/v1/auth/requests",
		"application/json",
		bytes.NewBufferString(`{"clientName":"Claude Code","purpose":"匹配未命中 Bangumi 条目"}`),
	)
	if err != nil {
		t.Fatalf("POST auth request: %v", err)
	}
	var created struct {
		RequestID        string `json:"requestId"`
		VerificationCode string `json:"verificationCode"`
		PollToken        string `json:"pollToken"`
		ApprovalURL      string `json:"approvalUrl"`
		ManualKeyURL     string `json:"manualKeyUrl"`
		PollURL          string `json:"pollUrl"`
	}
	_ = json.NewDecoder(createResp.Body).Decode(&created)
	createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated || created.RequestID == "" || created.PollToken == "" || len(created.VerificationCode) != 7 {
		t.Fatalf("unexpected created agent auth request: %d %+v", createResp.StatusCode, created)
	}
	if !strings.HasSuffix(created.ApprovalURL, "/agent-auth/"+created.RequestID) {
		t.Fatalf("unexpected approvalUrl: %s", created.ApprovalURL)
	}

	// 3. Unauthenticated visit to /agent-auth/{id} redirects to /login?redirect=/agent-auth/{id}
	noRedirectClient := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	pageResp, err := noRedirectClient.Get(created.ApprovalURL)
	if err != nil {
		t.Fatalf("GET approval page unauth: %v", err)
	}
	pageResp.Body.Close()
	if pageResp.StatusCode != http.StatusTemporaryRedirect || !strings.Contains(pageResp.Header.Get("Location"), "/login?redirect=") {
		t.Fatalf("expected login redirect for unauthenticated /agent-auth, got %d loc=%q", pageResp.StatusCode, pageResp.Header.Get("Location"))
	}

	// 4. Polling with wrong token -> 403; polling with right token while pending -> pending & no apiKey
	badPollReq, _ := http.NewRequest(http.MethodGet, created.PollURL, nil)
	badPollReq.Header.Set("X-Poll-Token", "wrong_token")
	badPollResp, _ := http.DefaultClient.Do(badPollReq)
	badPollResp.Body.Close()
	if badPollResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for bad poll token, got %d", badPollResp.StatusCode)
	}

	pendPollReq, _ := http.NewRequest(http.MethodGet, created.PollURL, nil)
	pendPollReq.Header.Set("X-Poll-Token", created.PollToken)
	pendPollResp, _ := http.DefaultClient.Do(pendPollReq)
	var pendBody map[string]any
	_ = json.NewDecoder(pendPollResp.Body).Decode(&pendBody)
	pendPollResp.Body.Close()
	if pendPollResp.StatusCode != http.StatusOK || pendBody["status"] != "pending" || pendBody["apiKey"] != nil {
		t.Fatalf("unexpected pending poll response: %d %+v", pendPollResp.StatusCode, pendBody)
	}

	// 5. Admin inspects request (no pollToken/apiKey exposed) and approves it
	adminGetReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/api/agent/auth-requests/"+created.RequestID, nil)
	adminGetReq.Header.Set("Cookie", cookie)
	adminGetResp, _ := http.DefaultClient.Do(adminGetReq)
	var adminGetBody map[string]any
	_ = json.NewDecoder(adminGetResp.Body).Decode(&adminGetBody)
	adminGetResp.Body.Close()
	if adminGetResp.StatusCode != http.StatusOK || adminGetBody["verificationCode"] != created.VerificationCode || adminGetBody["pollToken"] != nil {
		t.Fatalf("unexpected admin get auth-request: %d %+v", adminGetResp.StatusCode, adminGetBody)
	}

	approveReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/admin/api/agent/auth-requests/"+created.RequestID+"/approve",
		strings.NewReader(`{"keyName":"AI Agent · Claude Code"}`))
	approveReq.Header.Set("Cookie", cookie)
	approveReq.Header.Set("Content-Type", "application/json")
	approveResp, _ := http.DefaultClient.Do(approveReq)
	var approveBody struct {
		Status string `json:"status"`
		Key    string `json:"key"`
	}
	_ = json.NewDecoder(approveResp.Body).Decode(&approveBody)
	approveResp.Body.Close()
	if approveResp.StatusCode != http.StatusOK || approveBody.Status != "approved" || !strings.HasPrefix(approveBody.Key, "fk115_") {
		t.Fatalf("unexpected approve response: %d %+v", approveResp.StatusCode, approveBody)
	}

	// 6. Agent polls ONCE -> gets API Key, then second poll gets status=consumed with NO key!
	firstPollReq, _ := http.NewRequest(http.MethodGet, created.PollURL, nil)
	firstPollReq.Header.Set("X-Poll-Token", created.PollToken)
	firstPollResp, _ := http.DefaultClient.Do(firstPollReq)
	var firstPoll map[string]any
	_ = json.NewDecoder(firstPollResp.Body).Decode(&firstPoll)
	firstPollResp.Body.Close()
	gotKey, _ := firstPoll["apiKey"].(string)
	if firstPollResp.StatusCode != http.StatusOK || firstPoll["status"] != "approved" || gotKey != approveBody.Key {
		t.Fatalf("expected approved key on first poll, got %d %+v", firstPollResp.StatusCode, firstPoll)
	}

	secondPollReq, _ := http.NewRequest(http.MethodGet, created.PollURL, nil)
	secondPollReq.Header.Set("X-Poll-Token", created.PollToken)
	secondPollResp, _ := http.DefaultClient.Do(secondPollReq)
	var secondPoll map[string]any
	_ = json.NewDecoder(secondPollResp.Body).Decode(&secondPoll)
	secondPollResp.Body.Close()
	if secondPollResp.StatusCode != http.StatusOK || secondPoll["status"] != "consumed" || secondPoll["apiKey"] != nil {
		t.Fatalf("expected consumed without apiKey on second poll, got %d %+v", secondPollResp.StatusCode, secondPoll)
	}

	// 7. Minted API key immediately authenticates /api/agent/v1/overview
	ovReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/agent/v1/overview", nil)
	ovReq.Header.Set("Authorization", "Bearer "+gotKey)
	ovResp, _ := http.DefaultClient.Do(ovReq)
	ovResp.Body.Close()
	if ovResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on agent overview with minted key, got %d", ovResp.StatusCode)
	}

	// 8. Reject flow works and mints no key
	rejCreateResp, _ := http.Post(ts.URL+"/api/agent/v1/auth/requests", "application/json", strings.NewReader(`{"clientName":"RejectTest"}`))
	var rejCreated struct {
		RequestID string `json:"requestId"`
		PollToken string `json:"pollToken"`
		PollURL   string `json:"pollUrl"`
	}
	_ = json.NewDecoder(rejCreateResp.Body).Decode(&rejCreated)
	rejCreateResp.Body.Close()

	rejReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/admin/api/agent/auth-requests/"+rejCreated.RequestID+"/reject", nil)
	rejReq.Header.Set("Cookie", cookie)
	rejResp, _ := http.DefaultClient.Do(rejReq)
	rejResp.Body.Close()
	if rejResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on reject, got %d", rejResp.StatusCode)
	}
	rejPollReq, _ := http.NewRequest(http.MethodGet, rejCreated.PollURL, nil)
	rejPollReq.Header.Set("X-Poll-Token", rejCreated.PollToken)
	rejPollResp, _ := http.DefaultClient.Do(rejPollReq)
	var rejPoll map[string]any
	_ = json.NewDecoder(rejPollResp.Body).Decode(&rejPoll)
	rejPollResp.Body.Close()
	if rejPoll["status"] != "rejected" || rejPoll["apiKey"] != nil {
		t.Fatalf("expected rejected with no apiKey, got %+v", rejPoll)
	}

	// 9. Per-IP rate limiter kicks in after burst (2 already created above + 8 more = 10 allowed, 11th -> 429)
	got429 := false
	for i := 0; i < 12; i++ {
		r, err := http.Post(ts.URL+"/api/agent/v1/auth/requests", "application/json", strings.NewReader(`{"clientName":"Burst"}`))
		if err != nil {
			t.Fatalf("burst create %d: %v", i, err)
		}
		r.Body.Close()
		if r.StatusCode == http.StatusTooManyRequests {
			if r.Header.Get("Retry-After") == "" {
				t.Fatalf("429 missing Retry-After header")
			}
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatalf("expected per-IP 429 rate limit on /api/agent/v1/auth/requests")
	}
}

func TestV27BangumiLookupArchiveSummaryAndSourceGate(t *testing.T) {
	_, store, bgmSvc, ts := setupV27TestServer(t, "")
	cookie := claimAndLoginCookie(t, store)
	ctx := context.Background()

	// Build a minimal archive zip with a Chinese-translated book subject (NOT in local library).
	zipPath := filepath.Join(t.TempDir(), "dump-zh.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	zw := zip.NewWriter(zf)
	w, err := zw.Create("subject.jsonlines")
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	_, _ = w.Write([]byte(`{"id":404123,"type":1,"platform":1001,"series":true,"nsfw":false,"name":"ファミレス行こ。","name_cn":"去家庭餐厅吧。","summary":"第一行简中译文。\n第二行简中译文。","infobox":"","score":8.5,"rank":120}` + "\n"))
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	_ = zf.Close()

	if err := bgmSvc.ImportArchiveZip(ctx, zipPath, "dump-zh.zip", "2026-11-23T00:00:00Z"); err != nil {
		t.Fatalf("import archive zip: %v", err)
	}

	rawKey, _, err := store.CreateAPIKey(ctx, "UserscriptSummaryTest")
	if err != nil {
		t.Fatalf("create api key: %v", err)
	}

	lookup := func() map[string]any {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/bangumi/lookup?subject_id=404123", nil)
		req.Header.Set("X-API-Key", rawKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("lookup: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("lookup status=%d", resp.StatusCode)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}

	// 1. Default source (zh_cn): returns summary even when found=false!
	gotZH := lookup()
	if gotZH["found"] != false || gotZH["summarySource"] != bangumi.ArchiveSourceZH || gotZH["summary"] != "第一行简中译文。\n第二行简中译文。" {
		t.Fatalf("expected zh_cn summary even when not owned, got %+v", gotZH)
	}

	// 2. Switch archive source to "official" -> must NOT return summary
	if _, err := bgmSvc.SetArchiveSource(ctx, bangumi.ArchiveSourceOfficial, ""); err != nil {
		t.Fatalf("set official source: %v", err)
	}
	gotOff := lookup()
	if _, exists := gotOff["summary"]; exists {
		t.Fatalf("expected no summary when archive source is official, got %+v", gotOff)
	}

	// 3. Switch archive source to "custom" -> returns summary again
	if _, err := bgmSvc.SetArchiveSource(ctx, bangumi.ArchiveSourceCustom, "https://example.com/aux/latest.json"); err != nil {
		t.Fatalf("set custom source: %v", err)
	}
	gotCustom := lookup()
	if gotCustom["summarySource"] != bangumi.ArchiveSourceCustom || gotCustom["summary"] != "第一行简中译文。\n第二行简中译文。" {
		t.Fatalf("expected custom source summary, got %+v", gotCustom)
	}

	// 4. Verify rendered userscript contains v2.7.1 & summary translate button logic
	usReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/admin/api/bangumi/userscript.user.js", nil)
	usReq.Header.Set("Cookie", cookie)
	usResp, err := http.DefaultClient.Do(usReq)
	if err != nil {
		t.Fatalf("get userscript: %v", err)
	}
	usBytes, _ := io.ReadAll(usResp.Body)
	usResp.Body.Close()
	usBody := string(usBytes)
	for _, needle := range []string{"@version      2.7.1", "#mangaalpha-summary-bar", "显示简中翻译", "查看原文"} {
		if !strings.Contains(usBody, needle) {
			t.Fatalf("userscript missing %q", needle)
		}
	}
}
