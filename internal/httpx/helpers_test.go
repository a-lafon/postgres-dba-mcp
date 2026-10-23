package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

// newRegistry builds a registry on dsn (the pool opens no connection until a tool uses it).
func newRegistry(t *testing.T, dsn string, args ...string) (*config.Config, *server.Registry) {
	t.Helper()
	cfg, err := config.Parse(append(args, dsn), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return cfg, server.New(cfg, d, "test")
}

// start runs serve on a free loopback port and returns the port; at cleanup the server stops and must return nil.
func start(t *testing.T, cfg *config.Config, reg *server.Registry) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ln, cfg, reg) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve returned %v", err)
		}
	})
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	return port
}

// syncBuf is a bytes.Buffer safe for concurrent writers.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// sleepTool registers a tool that sleeps for d and answers "slept".
func sleepTool(reg *server.Registry, d time.Duration) {
	reg.Add(server.Tool{
		Group: "schema", Title: "Sleep",
		Def: &mcp.Tool{Name: "sleep", Description: "test tool", InputSchema: map[string]any{"type": "object"}},
		Handler: func(context.Context, json.RawMessage) (string, error) {
			time.Sleep(d)
			return "slept", nil
		},
	})
}
