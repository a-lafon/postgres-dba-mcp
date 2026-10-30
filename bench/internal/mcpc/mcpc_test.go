package mcpc

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain turns the test binary into a fake MCP stdio server when BENCH_FAKE_SERVER is set.
func TestMain(m *testing.M) {
	if os.Getenv("BENCH_FAKE_SERVER") != "" {
		s := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, &mcp.ServerOptions{Instructions: "be nice"})
		type in struct {
			Bytes int `json:"bytes,omitempty"`
			Sleep int `json:"sleep_ms,omitempty"`
		}
		mcp.AddTool(s, &mcp.Tool{Name: "blob", Description: "returns n bytes"}, func(ctx context.Context, _ *mcp.CallToolRequest, a in) (*mcp.CallToolResult, any, error) {
			time.Sleep(time.Duration(a.Sleep) * time.Millisecond)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", a.Bytes)}}}, nil, nil
		})
		_ = s.Run(context.Background(), &mcp.StdioTransport{})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func start(t *testing.T, limit int, timeout time.Duration) *Client {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c, err := startStdio(context.Background(), []string{exe}, []string{"BENCH_FAKE_SERVER=1"}, limit, timeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestCall(t *testing.T) {
	tests := []struct {
		name    string
		limit   int
		timeout time.Duration
		args    map[string]any
		want    error
		size    int
	}{
		{"small", MaxResponse, time.Minute, map[string]any{"bytes": 100}, nil, 100},
		{"timeout", MaxResponse, 200 * time.Millisecond, map[string]any{"bytes": 1, "sleep_ms": 3000}, ErrTimeout, 0},
		{"oversized", 1 << 20, time.Minute, map[string]any{"bytes": 2 << 20}, ErrOversized, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := start(t, tt.limit, tt.timeout)
			if c.Instructions != "be nice" {
				t.Errorf("instructions = %q", c.Instructions)
			}
			res, err := c.Call(context.Background(), "blob", tt.args)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if len(res.Text) != tt.size {
				t.Errorf("text size = %d, want %d", len(res.Text), tt.size)
			}
		})
	}
}

func TestTools(t *testing.T) {
	c := start(t, MaxResponse, time.Minute)
	tools, err := c.Tools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "blob" {
		t.Fatalf("tools = %v, err = %v", tools, err)
	}
}
