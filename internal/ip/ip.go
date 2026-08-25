// Package ip detects the network's public IPv4 address from multiple
// independent HTTPS sources. At least two sources must agree, otherwise
// detection fails so callers skip the run instead of applying a wrong address.
package ip

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Sources are queried concurrently; the first two that agree decide the IP.
var Sources = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://ipv4.icanhazip.com",
}

const (
	clientTimeout     = 6 * time.Second
	requiredAgreement = 2
)

type probe struct {
	url string
	ip  string
}

// Detect returns the current public IPv4 address or an error when fewer than
// two independent sources agree on it.
func Detect(ctx context.Context) (netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	ch := make(chan probe, len(Sources))
	for _, u := range Sources {
		go func(url string) { ch <- probe{url, fetch(ctx, url)} }(u)
	}

	var ok []probe
	for range Sources {
		if p := <-ch; p.ip != "" {
			ok = append(ok, p)
		}
	}
	if len(ok) < requiredAgreement {
		return netip.Addr{}, fmt.Errorf("public IP detection failed: only %d/%d sources responded", len(ok), len(Sources))
	}

	counts := map[string]int{}
	for _, p := range ok {
		counts[p.ip]++
	}
	best, bestN := "", 0
	for ipstr, n := range counts {
		if n > bestN {
			best, bestN = ipstr, n
		}
	}
	if bestN < requiredAgreement {
		return netip.Addr{}, fmt.Errorf("public IP sources disagree: %v",
			strings.Join(mustKeys(ok, counts), ", "))
	}
	return netip.ParseAddr(best)
}

func mustKeys(ok []probe, counts map[string]int) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range ok {
		if !seen[p.ip] {
			seen[p.ip] = true
			out = append(out, fmt.Sprintf("%s=%d", p.ip, counts[p.ip]))
		}
	}
	return out
}

func fetch(ctx context.Context, url string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	client := &http.Client{Timeout: clientTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return ""
	}
	ipstr := strings.TrimSpace(string(b))
	a, err := netip.ParseAddr(ipstr)
	if err != nil || !a.Is4() {
		return ""
	}
	return ipstr
}
