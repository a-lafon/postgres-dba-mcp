package httpx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

const (
	readHeaderTimeout = 10 * time.Second
	// readTimeout bounds reading one request, body included, so a client that
	// dribbles cannot hold a connection open; it does not bound the response.
	readTimeout = 30 * time.Second
	// idleTimeout is explicit because net/http falls back to ReadTimeout for an
	// idle kept-alive connection; 30 s would close one between two tool calls.
	idleTimeout   = 2 * time.Minute
	shutdownGrace = 10 * time.Second
)

// Serve listens on cfg.Host:cfg.Port and serves reg's MCP server over cfg.Transport until ctx is canceled,
// then drains in-flight requests for up to 10 seconds and closes the database pool.
func Serve(ctx context.Context, cfg *config.Config, reg *server.Registry) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	return serve(ctx, ln, cfg, reg)
}

func logf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// serve runs the HTTP server on ln; the Host/Origin allowlists use the port ln really listens on.
func serve(ctx context.Context, ln net.Listener, cfg *config.Config, reg *server.Registry) error {
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("listener address: %w", err)
	}
	getServer := func(*http.Request) *mcp.Server { return reg.MCP() }
	mux := http.NewServeMux()
	path := "/mcp"
	if cfg.Transport == config.SSE {
		path = "/sse"
		mux.Handle(path, mcp.NewSSEHandler(getServer, &mcp.SSEOptions{DisableLocalhostProtection: true}))
		logf("WARNING: the sse transport is deprecated; use --transport=streamable-http")
	} else {
		mux.Handle(path, mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{Stateless: true, DisableLocalhostProtection: true}))
	}
	g := newGuard(cfg.Host, port, cfg.AllowedHosts, cfg.AllowedOrigins, logf)
	srv := &http.Server{Handler: g.wrap(mux), ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, IdleTimeout: idleTimeout}
	if !config.IsLoopback(cfg.Host) {
		logf("WARNING: HTTP transport bound to %s:%s without authentication; anyone who can reach it can use the database with this role", cfg.Host, port)
	}
	logf("listening on http://%s%s", net.JoinHostPort(cfg.Host, port), path)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		reg.DB().Close()
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
	defer cancel()
	err = srv.Shutdown(sctx)
	if errors.Is(err, context.DeadlineExceeded) {
		// Long-lived SSE streams never drain: cut them after the grace period.
		err = srv.Close()
	}
	reg.DB().Close()
	if err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
