package httpserver

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	authFailThreshold   = 10
	authFailWindow      = 5 * time.Minute
	authBaseBlock       = 30 * time.Second
	authMaxBlock        = 5 * time.Minute
	authLimiterMaxHosts = 4096
)

var defaultTrustedProxyCIDRs = []string{
	"127.0.0.0/8",
	"::1/128",
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"fc00::/7",
}

type trustedProxyChecker struct {
	prefixes []netip.Prefix
}

func newTrustedProxyChecker(raw string) *trustedProxyChecker {
	raw = strings.TrimSpace(raw)
	if strings.EqualFold(raw, "none") || strings.EqualFold(raw, "off") || strings.EqualFold(raw, "false") {
		return &trustedProxyChecker{}
	}
	var items []string
	if raw == "" {
		items = defaultTrustedProxyCIDRs
	} else {
		for _, part := range strings.Split(raw, ",") {
			if p := strings.TrimSpace(part); p != "" {
				items = append(items, p)
			}
		}
	}
	checker := &trustedProxyChecker{prefixes: make([]netip.Prefix, 0, len(items))}
	for _, item := range items {
		if pfx, err := netip.ParsePrefix(item); err == nil {
			checker.prefixes = append(checker.prefixes, pfx.Masked())
			continue
		}
		if addr, err := netip.ParseAddr(item); err == nil {
			addr = addr.Unmap()
			checker.prefixes = append(checker.prefixes, netip.PrefixFrom(addr, addr.BitLen()))
		}
	}
	return checker
}

func (c *trustedProxyChecker) isTrustedAddr(addr netip.Addr) bool {
	if c == nil || !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	for _, pfx := range c.prefixes {
		if pfx.Contains(addr) {
			return true
		}
	}
	return false
}

func parseRemoteAddrIP(remoteAddr string) (netip.Addr, bool) {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if remoteAddr == "" {
		return netip.Addr{}, false
	}
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if zone := strings.IndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

func (c *trustedProxyChecker) isTrustedRequest(r *http.Request) bool {
	if c == nil || r == nil {
		return false
	}
	addr, ok := parseRemoteAddrIP(r.RemoteAddr)
	return ok && c.isTrustedAddr(addr)
}

func (c *trustedProxyChecker) clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	peerAddr, peerOK := parseRemoteAddrIP(r.RemoteAddr)
	if !peerOK {
		return strings.TrimSpace(r.RemoteAddr)
	}
	if !c.isTrustedAddr(peerAddr) {
		return peerAddr.String()
	}
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		parts := strings.Split(xff, ",")
		// Walk right-to-left and stop at the rightmost non-trusted proxy IP.
		for i := len(parts) - 1; i >= 0; i-- {
			if addr, ok := parseRemoteAddrIP(parts[i]); ok {
				if !c.isTrustedAddr(addr) {
					return addr.String()
				}
			}
		}
		for _, part := range parts {
			if addr, ok := parseRemoteAddrIP(part); ok {
				return addr.String()
			}
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		if addr, ok := parseRemoteAddrIP(xri); ok {
			return addr.String()
		}
	}
	return peerAddr.String()
}

func (s *Server) isHTTPSRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	if s != nil && s.trustedProxies != nil && !s.trustedProxies.isTrustedRequest(r) {
		return false
	}
	proto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	return strings.EqualFold(proto, "https")
}

// --- Auth Brute-Force Rate Limiter ---

type authFailRecord struct {
	failures    int
	windowStart time.Time
	blockedTil  time.Time
	lastSeen    time.Time
}

type authRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*authFailRecord
	nowFunc func() time.Time
}

func newAuthRateLimiter() *authRateLimiter {
	return &authRateLimiter{
		entries: make(map[string]*authFailRecord),
		nowFunc: time.Now,
	}
}

func (l *authRateLimiter) now() time.Time {
	if l != nil && l.nowFunc != nil {
		return l.nowFunc()
	}
	return time.Now()
}

// Allow reports whether clientKey may attempt authentication; when false it returns retryAfter.
func (l *authRateLimiter) Allow(clientKey string) (bool, time.Duration) {
	if l == nil || clientKey == "" {
		return true, 0
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.entries[clientKey]
	if !ok {
		return true, 0
	}
	if rec.blockedTil.After(now) {
		return false, rec.blockedTil.Sub(now)
	}
	if now.Sub(rec.windowStart) > authFailWindow && now.Sub(rec.lastSeen) > authFailWindow {
		delete(l.entries, clientKey)
	}
	return true, 0
}

// RecordFailure records a failed authentication attempt for clientKey.
func (l *authRateLimiter) RecordFailure(clientKey string) {
	if l == nil || clientKey == "" {
		return
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) >= authLimiterMaxHosts {
		for k, v := range l.entries {
			if now.After(v.blockedTil) && now.Sub(v.lastSeen) > authFailWindow {
				delete(l.entries, k)
			}
		}
		if len(l.entries) >= authLimiterMaxHosts {
			l.entries = make(map[string]*authFailRecord)
		}
	}
	rec, ok := l.entries[clientKey]
	if !ok || (now.Sub(rec.windowStart) > authFailWindow && now.After(rec.blockedTil)) {
		rec = &authFailRecord{windowStart: now}
		l.entries[clientKey] = rec
	}
	rec.failures++
	rec.lastSeen = now
	if rec.failures >= authFailThreshold {
		shift := rec.failures - authFailThreshold
		if shift > 4 {
			shift = 4
		}
		blockDur := authBaseBlock << shift
		if blockDur > authMaxBlock {
			blockDur = authMaxBlock
		}
		rec.blockedTil = now.Add(blockDur)
	}
}

// RecordSuccess clears any accumulated failure history for clientKey.
func (l *authRateLimiter) RecordSuccess(clientKey string) {
	if l == nil || clientKey == "" {
		return
	}
	l.mu.Lock()
	delete(l.entries, clientKey)
	l.mu.Unlock()
}

func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	secs := int(retryAfter.Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "TOO_MANY_REQUESTS",
		fmt.Sprintf("登录失败次数过多，请在 %d 秒后重试。", secs))
}

// --- Security Headers Middleware ---

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if h.Get("X-Content-Type-Options") == "" {
			h.Set("X-Content-Type-Options", "nosniff")
		}
		if h.Get("X-Frame-Options") == "" {
			h.Set("X-Frame-Options", "SAMEORIGIN")
		}
		if h.Get("Referrer-Policy") == "" {
			h.Set("Referrer-Policy", "same-origin")
		}
		next.ServeHTTP(w, r)
	})
}

// --- Same-Origin CSRF Guard (LAN HTTP & Native Client Safe) ---

func isUnsafeHTTPMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// checkSameOriginCSRF blocks cross-origin browser state-changing requests while remaining
// 100% compatible with plain LAN HTTP (where browsers omit Sec-Fetch-Site) and native
// Komga clients (Mihon/Koharia/curl) that do not send browser Origin/Referer headers.
func checkSameOriginCSRF(r *http.Request) bool {
	if r == nil || !isUnsafeHTTPMethod(r.Method) {
		return true
	}
	if sfs := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")); sfs != "" {
		if strings.EqualFold(sfs, "cross-site") {
			return false
		}
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		if strings.EqualFold(origin, "null") {
			return false
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" {
			return false
		}
		return sameHostPort(u.Scheme, u.Host, r.Host)
	}
	if referer := strings.TrimSpace(r.Header.Get("Referer")); referer != "" {
		u, err := url.Parse(referer)
		if err != nil || u.Host == "" {
			return false
		}
		return sameHostPort(u.Scheme, u.Host, r.Host)
	}
	return true
}

func sameHostPort(originScheme, originHost, reqHost string) bool {
	return normalizeHostPort(originScheme, originHost) == normalizeHostPort(originScheme, reqHost)
}

func normalizeHostPort(scheme, hostPort string) string {
	hostPort = strings.ToLower(strings.TrimSpace(hostPort))
	if hostPort == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		// No explicit port
		host = strings.Trim(hostPort, "[]")
		port = ""
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

func sameSchemeAndHost(rawA, rawB string) bool {
	uA, errA := url.Parse(strings.TrimSpace(rawA))
	uB, errB := url.Parse(strings.TrimSpace(rawB))
	if errA != nil || errB != nil || uA.Host == "" || uB.Host == "" {
		return false
	}
	sA := strings.ToLower(uA.Scheme)
	sB := strings.ToLower(uB.Scheme)
	if sA != sB {
		return false
	}
	return normalizeHostPort(sA, uA.Host) == normalizeHostPort(sB, uB.Host)
}
