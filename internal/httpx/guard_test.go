package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func ptr(s string) *string { return &s }

func TestGuard(t *testing.T) {
	tests := []struct {
		name    string
		bind    string
		hosts   []string
		origins []string
		host    string
		origin  *string // nil: no Origin header
		size    int
		status  int
		refusal string // expected 403 body
	}{
		{name: "localhost", host: "localhost:8000", status: 200},
		{name: "ipv4", host: "127.0.0.1:8000", status: 200},
		{name: "ipv6", host: "[::1]:8000", status: 200},
		{name: "case-insensitive", host: "LocalHost:8000", status: 200},
		{name: "rebinding host", host: "evil.example:8000", status: 403, refusal: `forbidden: Host "evil.example:8000" is not allowed`},
		{name: "port mismatch", host: "localhost:9999", status: 403},
		{name: "no port", host: "localhost", status: 403},
		{name: "wildcard bind is not a host", bind: "0.0.0.0", host: "0.0.0.0:8000", status: 403},
		{name: "bind host", bind: "192.168.1.5", host: "192.168.1.5:8000", status: 200},
		{name: "extra host any port", hosts: []string{"db-mcp.internal"}, host: "db-mcp.internal:8000", status: 200},
		{name: "extra host any port, other port", hosts: []string{"db-mcp.internal"}, host: "DB-MCP.internal:1234", status: 200},
		{name: "extra host with port", hosts: []string{"localhost:9000"}, host: "localhost:9000", status: 200},
		{name: "extra host with port, other port", hosts: []string{"a.internal:9000"}, host: "a.internal:9001", status: 403},
		{name: "foreign origin", host: "localhost:8000", origin: ptr("https://evil.example"), status: 403, refusal: `forbidden: Origin "https://evil.example" is not allowed`},
		{name: "null origin", host: "localhost:8000", origin: ptr("null"), status: 403},
		{name: "local origin", host: "localhost:8000", origin: ptr("http://localhost:8000"), status: 200},
		{name: "local origin wrong port", host: "localhost:8000", origin: ptr("http://localhost:1"), status: 403},
		{name: "extra origin", origins: []string{"https://app.example"}, host: "localhost:8000", origin: ptr("https://app.example"), status: 200},
		{name: "1 MiB body", host: "localhost:8000", size: 1 << 20, status: 200},
		{name: "oversized body", host: "localhost:8000", size: 2 << 20, status: 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs syncBuf
			reached := false
			g := newGuard(tt.bind, "8000", tt.hosts, tt.origins, func(f string, _ ...any) { _, _ = logs.Write([]byte(f)) })
			h := g.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
			req := httptest.NewRequest("POST", "/mcp", strings.NewReader(strings.Repeat("x", tt.size)))
			req.Host = tt.host
			if tt.origin != nil {
				req.Header.Set("Origin", *tt.origin)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if reached != (tt.status == 200) {
				t.Errorf("handler reached = %v", reached)
			}
			if tt.refusal != "" {
				got, _ := io.ReadAll(rec.Body)
				if string(got) != tt.refusal || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
					t.Errorf("body = %q, content type %q", got, rec.Header().Get("Content-Type"))
				}
				if logs.String() == "" {
					t.Error("refusal not logged")
				}
			}
		})
	}
}

func TestGuardTruncatesLoggedHeader(t *testing.T) {
	var logged string
	g := newGuard("", "8000", nil, nil, func(f string, a ...any) { logged = a[1].(string) })
	req := httptest.NewRequest("GET", "/mcp", nil)
	req.Host = strings.Repeat("h", 500)
	g.wrap(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), req)
	if n := strings.Count(logged, "h"); n != 200 {
		t.Errorf("logged %d bytes of the header, want 200", n)
	}
}

// TestBothTransportsGuarded checks that the guard wraps /mcp and /sse alike and that other paths are 404.
func TestBothTransportsGuarded(t *testing.T) {
	for _, tr := range []struct{ transport, path string }{{"streamable-http", "/mcp"}, {"sse", "/sse"}} {
		t.Run(tr.transport, func(t *testing.T) {
			cfg, reg := newRegistry(t, "postgres://u@h/db", "--transport="+tr.transport)
			port := start(t, cfg, reg)
			do := func(path, host, origin string) int {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel() // a valid SSE request would stream forever
				req, err := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:"+port+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				if host != "" {
					req.Host = host
				}
				if origin != "" {
					req.Header.Set("Origin", origin)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_ = resp.Body.Close()
				return resp.StatusCode
			}
			if got := do(tr.path, "evil.example:"+port, ""); got != 403 {
				t.Errorf("foreign Host: %d, want 403", got)
			}
			if got := do(tr.path, "", "https://evil.example"); got != 403 {
				t.Errorf("foreign Origin: %d, want 403", got)
			}
			if got := do("/other", "", ""); got != 404 {
				t.Errorf("unknown path: %d, want 404", got)
			}
		})
	}
}
