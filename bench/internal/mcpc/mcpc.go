// Package mcpc is the bench's MCP client: it starts a server over stdio or connects to one over streamable HTTP,
// lists its tools and calls them with a per-call timeout and a response-size cap.
package mcpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// CallTimeout is the longest a single tools/call may take.
	CallTimeout = 120 * time.Second
	// MaxResponse is the largest response frame accepted, in bytes.
	MaxResponse = 16 << 20
)

// legacyProtocol is the protocol version sent in `initialize`.
const legacyProtocol = "2025-11-25"

// ErrTimeout is returned by Call when the call exceeded its timeout.
var ErrTimeout = errors.New("call timed out")

// ErrOversized is returned by Call when the response exceeded MaxResponse.
var ErrOversized = errors.New("response exceeds the size cap")

// Result is the outcome of one tools/call.
type Result struct {
	Text    string        // all text content, joined
	IsError bool          // the server flagged the result as an error
	Latency time.Duration // wall time of the call
}

// Client is one connected MCP session.
type Client struct {
	session *mcp.ClientSession
	cmd     *exec.Cmd
	stdin   io.Closer
	over    *atomic.Bool
	timeout time.Duration
	limit   int
	// Info is the server's name and version from the initialize handshake.
	Info string
	// Instructions is the server's instructions text, empty when it has none.
	Instructions string
}

// frameCounter flags the stream when a single newline-delimited frame reaches the cap, which is when the SDK gives up.
type frameCounter struct {
	r     io.ReadCloser
	n     int
	limit int
	over  *atomic.Bool
}

func (f *frameCounter) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	for _, b := range p[:n] {
		if b == '\n' {
			f.n = 0
			continue
		}
		f.n++
		if f.n >= f.limit {
			f.over.Store(true)
		}
	}
	return n, err
}

func (f *frameCounter) Close() error { return f.r.Close() }

// StartStdio starts argv (with extra environment entries env, as KEY=VALUE) and connects to it over stdio.
func StartStdio(ctx context.Context, argv, env []string) (*Client, error) {
	return startStdio(ctx, argv, env, MaxResponse, CallTimeout)
}

// StartStdioLimit is StartStdio with another response-size cap, for measures that need to read answers the default
// cap refuses.
func StartStdioLimit(ctx context.Context, argv, env []string, limit int) (*Client, error) {
	return startStdio(ctx, argv, env, limit, CallTimeout)
}

func startStdio(ctx context.Context, argv, env []string, limit int, timeout time.Duration) (*Client, error) {
	if len(argv) == 0 {
		return nil, errors.New("start server: empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(cmd.Environ(), env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", argv[0], err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", argv[0], err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", argv[0], err)
	}
	over := new(atomic.Bool)
	tr := &mcp.IOTransport{Reader: &frameCounter{r: stdout, limit: limit, over: over}, Writer: stdin, MaxLineLength: limit}
	c := &Client{cmd: cmd, stdin: stdin, over: over, timeout: timeout, limit: limit}
	if err := c.connect(ctx, tr); err != nil {
		c.Close()
		return nil, fmt.Errorf("initialize %s: %w", argv[0], err)
	}
	return c, nil
}

// ConnectHTTP connects to a streamable-HTTP MCP endpoint, for example http://127.0.0.1:8000/mcp.
func ConnectHTTP(ctx context.Context, endpoint string) (*Client, error) {
	c := &Client{over: new(atomic.Bool), timeout: CallTimeout, limit: MaxResponse}
	tr := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &http.Client{}}
	if err := c.connect(ctx, tr); err != nil {
		return nil, fmt.Errorf("connect %s: %w", endpoint, err)
	}
	return c, nil
}

func (c *Client) connect(ctx context.Context, tr mcp.Transport) error {
	cl := mcp.NewClient(&mcp.Implementation{Name: "bench", Version: "0"}, nil)
	// The legacy `initialize` handshake: the SDK's default first tries server/discover (2026-07-28), which
	// crystaldba's mcp 1.6.0 never answers, and the spec measures cold start up to the initialize response.
	s, err := cl.Connect(ctx, tr, &mcp.ClientSessionOptions{ProtocolVersion: legacyProtocol})
	if err != nil {
		return err
	}
	c.session = s
	if r := s.InitializeResult(); r != nil {
		c.Instructions = r.Instructions
		if r.ServerInfo != nil {
			c.Info = r.ServerInfo.Name + " " + r.ServerInfo.Version
		}
	}
	return nil
}

// Pid returns the process id of a stdio server, or 0.
func (c *Client) Pid() int {
	if c.cmd == nil {
		return 0
	}
	return c.cmd.Process.Pid
}

// Tools returns the server's tools, following pagination.
func (c *Client) Tools(ctx context.Context) ([]*mcp.Tool, error) {
	var tools []*mcp.Tool
	for t, err := range c.session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		tools = append(tools, t)
	}
	return tools, nil
}

// Call runs one tool. A protocol or transport failure returns an error (ErrTimeout and ErrOversized are
// distinguishable with errors.Is); a result flagged isError is a normal Result.
func (c *Client) Call(ctx context.Context, name string, args map[string]any) (Result, error) {
	cctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	start := time.Now()
	res, err := c.session.CallTool(cctx, &mcp.CallToolParams{Name: name, Arguments: args})
	lat := time.Since(start)
	if err != nil {
		switch {
		case c.over.Load():
			return Result{Latency: lat}, fmt.Errorf("call %s: %w", name, ErrOversized)
		case errors.Is(cctx.Err(), context.DeadlineExceeded):
			return Result{Latency: lat}, fmt.Errorf("call %s: %w", name, ErrTimeout)
		}
		return Result{Latency: lat}, fmt.Errorf("call %s: %w", name, err)
	}
	var sb strings.Builder
	for _, ct := range res.Content {
		if t, ok := ct.(*mcp.TextContent); ok {
			sb.WriteString(t.Text)
		}
	}
	if sb.Len() > c.limit {
		return Result{Latency: lat}, fmt.Errorf("call %s: %w", name, ErrOversized)
	}
	return Result{Text: sb.String(), IsError: res.IsError, Latency: lat}, nil
}

// Close ends the session and, for a stdio server, stops its process.
func (c *Client) Close() {
	if c.session != nil {
		_ = c.session.Close()
	}
	if c.cmd == nil {
		return
	}
	_ = c.stdin.Close()
	done := make(chan struct{})
	go func() { _ = c.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
	}
}
