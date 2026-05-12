// Package probe runs a single read against a well-known endpoint to
// confirm the tunnel is carrying traffic and to surface the apparent
// public IP. Used by the Connected screen to fill the "public ip" /
// "rtt" fields that would otherwise stay as "—".
//
// The probe is deliberately minimal: one HTTPS GET to Cloudflare's
// `cdn-cgi/trace` endpoint, which returns plaintext lines of the form
// `key=value`. We pull `ip=` and `loc=` (the Cloudflare colo / country
// hint). Anything heavier (ipify, ifconfig.co) returns JSON that needs
// extra parsing for the same data, and Cloudflare is one of the few
// endpoints reliably reachable from inside most tunnels.
package probe

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// Result is what one successful probe carries back. RTT is wall-clock
// from request start to response body close — close enough to a
// round-trip estimate for a UI badge, but not a precision measurement.
type Result struct {
	IP      string
	Country string // 2-letter Cloudflare colo country, e.g. "DE" / "US"
	RTT     time.Duration
	At      time.Time
}

// defaultEndpoint is the Cloudflare trace endpoint. Stable for years,
// returns plaintext, IPv4 and IPv6, no auth, no rate-limit issues for
// the volumes we generate (one request every ~15s per running TUI).
const defaultEndpoint = "https://1.1.1.1/cdn-cgi/trace"

// Run performs one probe against defaultEndpoint with the given
// context as both the dial deadline and the overall request budget.
// Returns the parsed result on success. Callers schedule retries
// themselves; this function intentionally never retries internally —
// fast failure is the right behaviour for a "live" badge.
func Run(ctx context.Context) (Result, error) {
	return RunAgainst(ctx, defaultEndpoint)
}

// RunAgainst is the same as Run with a caller-chosen endpoint URL.
// Split out so tests can point at an httptest.Server without rewriting
// the parser.
func RunAgainst(ctx context.Context, url string) (Result, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			// Force a fresh dial so the probe really crosses the
			// network on each call — connection reuse would mask a
			// tunnel that just went down between probes.
			DisableKeepAlives: true,
			DialContext: (&net.Dialer{
				Timeout: 3 * time.Second,
			}).DialContext,
		},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, err
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, errors.New("probe: unexpected status " + resp.Status)
	}

	r := Result{At: start, RTT: time.Since(start)}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key, val := line[:eq], line[eq+1:]
		switch key {
		case "ip":
			r.IP = val
		case "loc":
			r.Country = val
		}
	}
	if r.IP == "" {
		return Result{}, errors.New("probe: response missing ip= line")
	}
	return r, nil
}
