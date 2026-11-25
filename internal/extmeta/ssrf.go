package extmeta

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPTimeout = 30 * time.Second
	maxRedirects       = 5
)

var (
	// ErrSSRFBlocked is returned when a URL or resolved IP points to a non-public or disallowed target.
	ErrSSRFBlocked = errors.New("request blocked by SSRF policy")
	// ErrInvalidURL is returned when a URL uses an unsupported scheme or lacks a valid host.
	ErrInvalidURL = errors.New("invalid URL: only http and https with a host are allowed")
	// ErrInvalidProxyURL is returned when a proxy URL is malformed or uses an unsupported scheme.
	ErrInvalidProxyURL = errors.New("invalid proxy URL: must use http://, https://, socks5://, or socks5h:// with host")
	// ErrTooManyRedirects is returned when HTTP redirects exceed the maximum allowed hops.
	ErrTooManyRedirects = errors.New("stopped after too many redirects")
)

var nonPublicIPv4Prefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "This" network (RFC 1122)
	netip.MustParsePrefix("100.64.0.0/10"),   // CGNAT / Shared Address Space (RFC 6598)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF Protocol Assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // Benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // Reserved & limited broadcast
}

var nonPublicIPv6Prefixes = []netip.Prefix{
	netip.MustParsePrefix("64:ff9b::/96"),   // NAT64 well-known prefix
	netip.MustParsePrefix("64:ff9b:1::/48"), // NAT64 local-use prefix
	netip.MustParsePrefix("100::/64"),       // Discard-only prefix
	netip.MustParsePrefix("2001:2::/48"),    // Benchmarking
	netip.MustParsePrefix("2001:db8::/32"),  // Documentation
	netip.MustParsePrefix("fec0::/10"),      // Deprecated site-local
}

type safeClientOptions struct {
	allowLoopbackForTest bool
}

// NewSafeHTTPClient builds an *http.Client hardened against SSRF attacks.
//
// In direct mode (proxyURL == ""), DialContext resolves every IP for the target
// host and rejects loopback, RFC1918/ULA private, link-local, unspecified,
// multicast, CGNAT (100.64.0.0/10), and other non-public ranges before dialing.
//
// When an explicit proxyURL is supplied, only basic scheme/host validation is
// performed on the proxy URL, and every request/redirect URL is checked for
// allowed http/https scheme and safe host/literal IP.
//
// Redirects are limited to at most 5 hops, validated at each hop, and strip
// Authorization, Proxy-Authorization, and Cookie headers when crossing hosts.
func NewSafeHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	return newSafeHTTPClient(proxyURL, timeout, safeClientOptions{})
}

func newSafeHTTPClient(proxyURL string, timeout time.Duration, opts safeClientOptions) *http.Client {
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}

	parsedProxy, err := validateProxyURL(proxyURL)
	if err != nil {
		return &http.Client{
			Timeout:   timeout,
			Transport: errRoundTripper{err: err},
		}
	}

	var base *http.Transport
	if dt, ok := http.DefaultTransport.(*http.Transport); ok {
		base = dt.Clone()
	} else {
		base = &http.Transport{}
	}
	base.TLSHandshakeTimeout = 15 * time.Second
	base.ResponseHeaderTimeout = min(timeout, 30*time.Second)
	base.IdleConnTimeout = 90 * time.Second

	if parsedProxy != nil {
		base.Proxy = http.ProxyURL(parsedProxy)
	} else {
		base.Proxy = nil
		dialer := &net.Dialer{
			Timeout:   min(timeout, 15*time.Second),
			KeepAlive: 30 * time.Second,
		}
		base.DialContext = makeSafeDialContext(dialer, net.DefaultResolver, opts.allowLoopbackForTest)
	}

	rt := &safeRoundTripper{
		next:                 base,
		allowLoopbackForTest: opts.allowLoopbackForTest,
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: rt,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return fmt.Errorf("%w (max %d)", ErrTooManyRedirects, maxRedirects)
			}
			if err := validateTargetURL(req.URL, opts.allowLoopbackForTest); err != nil {
				return err
			}
			if len(via) > 0 {
				prev := via[len(via)-1]
				if !sameHostOrigin(prev.URL, req.URL) || (strings.EqualFold(prev.URL.Scheme, "https") && strings.EqualFold(req.URL.Scheme, "http")) {
					req.Header.Del("Authorization")
					req.Header.Del("Proxy-Authorization")
					req.Header.Del("Cookie")
				}
			}
			return nil
		},
	}
}

type errRoundTripper struct {
	err error
}

func (e errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, e.err
}

type safeRoundTripper struct {
	next                 *http.Transport
	allowLoopbackForTest bool
}

func (s *safeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, ErrInvalidURL
	}
	if err := validateTargetURL(req.URL, s.allowLoopbackForTest); err != nil {
		return nil, err
	}
	return s.next.RoundTrip(req)
}

func (s *safeRoundTripper) CloseIdleConnections() {
	if s.next != nil {
		s.next.CloseIdleConnections()
	}
}

func validateProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil, ErrInvalidProxyURL
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
		return u, nil
	default:
		return nil, ErrInvalidProxyURL
	}
}

func validateHTTPURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrInvalidURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return nil, ErrInvalidURL
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("%w: unsupported scheme %q", ErrInvalidURL, u.Scheme)
	}
	return u, nil
}

func validateTargetURL(u *url.URL, allowLoopback bool) error {
	if u == nil {
		return ErrInvalidURL
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: unsupported scheme %q", ErrSSRFBlocked, u.Scheme)
	}
	if u.Hostname() == "" {
		return ErrInvalidURL
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return ErrInvalidURL
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if !isPublicAddr(addr, allowLoopback) {
			return fmt.Errorf("%w: disallowed IP %s", ErrSSRFBlocked, addr.String())
		}
		return nil
	}
	if !allowLoopback && isLoopbackHostname(host) {
		return fmt.Errorf("%w: disallowed host %q", ErrSSRFBlocked, host)
	}
	return nil
}

func isLoopbackHostname(host string) bool {
	return host == "localhost" ||
		strings.HasSuffix(host, ".localhost") ||
		host == "localhost.localdomain"
}

type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

func makeSafeDialContext(dialer *net.Dialer, resolver ipResolver, allowLoopback bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid address %q: %v", ErrSSRFBlocked, addr, err)
		}
		host = strings.TrimSuffix(strings.TrimSpace(host), ".")
		if host == "" {
			return nil, fmt.Errorf("%w: empty host", ErrSSRFBlocked)
		}

		// Fast path for literal IP addresses (avoids unnecessary DNS calls).
		if parsed, parseErr := netip.ParseAddr(host); parseErr == nil {
			if !isPublicAddr(parsed, allowLoopback) {
				return nil, fmt.Errorf("%w: disallowed IP %s", ErrSSRFBlocked, parsed.String())
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(parsed.Unmap().String(), port))
		}

		if !allowLoopback && isLoopbackHostname(strings.ToLower(host)) {
			return nil, fmt.Errorf("%w: disallowed host %q", ErrSSRFBlocked, host)
		}

		resolved, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(resolved) == 0 {
			return nil, fmt.Errorf("%w: no IP addresses resolved for %q", ErrSSRFBlocked, host)
		}

		validated := make([]netip.Addr, 0, len(resolved))
		for _, ipAddr := range resolved {
			a, ok := netip.AddrFromSlice(ipAddr.IP)
			if !ok {
				return nil, fmt.Errorf("%w: invalid resolved IP for %q", ErrSSRFBlocked, host)
			}
			if ipAddr.Zone != "" {
				a = a.WithZone(ipAddr.Zone)
			}
			if !isPublicAddr(a, allowLoopback) {
				return nil, fmt.Errorf("%w: host %q resolved to non-public IP %s", ErrSSRFBlocked, host, a.String())
			}
			validated = append(validated, a.Unmap())
		}

		var lastErr error
		for _, target := range validated {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(target.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

func isPublicAddr(addr netip.Addr, allowLoopback bool) bool {
	if !addr.IsValid() {
		return false
	}
	// Reject scoped non-global IPv6 addresses with zones unless loopback test mode allows them.
	if addr.Zone() != "" && !allowLoopback {
		return false
	}
	addr = addr.Unmap()
	if addr.IsUnspecified() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsInterfaceLocalMulticast() || addr.IsMulticast() {
		return false
	}
	if addr.IsLoopback() {
		return allowLoopback
	}
	if addr.Is4() {
		for _, pfx := range nonPublicIPv4Prefixes {
			if pfx.Contains(addr) {
				return false
			}
		}
		return addr.IsGlobalUnicast()
	}
	for _, pfx := range nonPublicIPv6Prefixes {
		if pfx.Contains(addr) {
			return false
		}
	}
	return addr.IsGlobalUnicast()
}

func sameHostOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	return strings.EqualFold(a.Hostname(), b.Hostname()) && effectivePort(a) == effectivePort(b)
}

func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 {
			return strconv.Itoa(n)
		}
		return p
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return "80"
}
