// Package httpx serves the MCP server over the HTTP transports, behind a Host/Origin guard that blocks DNS rebinding.
package httpx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

const (
	maxBody   = 1 << 20 // request body cap in bytes
	maxLogged = 200     // header bytes kept in a refusal log line
)

// guard holds the allowlists of one listener, all lower-case.
type guard struct {
	hosts   map[string]bool // host:port
	anyPort map[string]bool // host, any port
	origins map[string]bool
	logf    func(format string, args ...any)
}

// newGuard builds the guard for a listener bound to bindHost:port.
func newGuard(bindHost, port string, extraHosts, extraOrigins []string, logf func(string, ...any)) *guard {
	g := &guard{hosts: map[string]bool{}, anyPort: map[string]bool{}, origins: map[string]bool{}, logf: logf}
	hosts := []string{"localhost", "127.0.0.1", "::1"}
	if ip := net.ParseIP(bindHost); bindHost != "" && (ip == nil || !ip.IsUnspecified()) {
		hosts = append(hosts, bindHost)
	}
	for _, h := range hosts {
		hp := strings.ToLower(net.JoinHostPort(h, port))
		g.hosts[hp] = true
		g.origins["http://"+hp] = true
	}
	for _, h := range extraHosts {
		h = strings.ToLower(h)
		if _, _, err := net.SplitHostPort(h); err == nil {
			g.hosts[h] = true
		} else {
			g.anyPort[strings.Trim(h, "[]")] = true
		}
	}
	for _, o := range extraOrigins {
		g.origins[strings.ToLower(o)] = true
	}
	return g
}

func (g *guard) hostAllowed(host string) bool {
	host = strings.ToLower(host)
	if g.hosts[host] {
		return true
	}
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	return g.anyPort[strings.Trim(name, "[]")]
}

// wrap returns next behind the Host check, the Origin check and the body cap.
func (g *guard) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.hostAllowed(r.Host) {
			g.refuse(w, "Host", r.Host)
			return
		}
		if vals, ok := r.Header["Origin"]; ok {
			for _, o := range vals {
				if !g.origins[strings.ToLower(o)] {
					g.refuse(w, "Origin", o)
					return
				}
			}
		}
		if r.Body != nil && r.Body != http.NoBody {
			// Read the capped body here so an oversized one gets 413 before the MCP handler sees it.
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			if err != nil {
				http.Error(w, "cannot read request body", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		next.ServeHTTP(w, r)
	})
}

func (g *guard) refuse(w http.ResponseWriter, header, value string) {
	if len(value) > maxLogged {
		value = value[:maxLogged]
	}
	g.logf("refused request: %s %q is not allowed", header, value)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusForbidden)
	_, _ = fmt.Fprintf(w, "forbidden: %s %q is not allowed", header, value) //nolint:gosec // G705: text/plain with nosniff, never rendered as HTML
}
