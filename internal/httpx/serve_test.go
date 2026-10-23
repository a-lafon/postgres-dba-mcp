package httpx

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestShutdownLetsInFlightCallFinish(t *testing.T) {
	cfg, reg := newRegistry(t, "postgres://u@h/db", "--transport=streamable-http")
	sleepTool(reg, 2*time.Second)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- serve(ctx, ln, cfg, reg) }()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: "http://" + ln.Addr().String() + "/mcp", DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()

	type result struct {
		res *mcp.CallToolResult
		err error
	}
	called := make(chan result, 1)
	go func() {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "sleep"})
		called <- result{res, err}
	}()
	time.Sleep(500 * time.Millisecond) // the call is in flight
	cancel()                           // what the signal handler does

	r := <-called
	if r.err != nil || r.res.IsError || r.res.Content[0].(*mcp.TextContent).Text != "slept" {
		t.Fatalf("in-flight call = %+v, %v", r.res, r.err)
	}
	if err := <-served; err != nil {
		t.Fatalf("serve returned %v, want nil", err)
	}
}
