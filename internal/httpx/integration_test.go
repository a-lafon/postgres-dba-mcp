//go:build integration

package httpx

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/schema"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

var duration = regexp.MustCompile(`[\d.]+ m?s$`)

// callListSchemas connects over tr and returns the text of list_schemas.
func callListSchemas(ctx context.Context, tr mcp.Transport) (string, error) {
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, tr, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_schemas"})
	if err != nil {
		return "", err
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if res.IsError {
		return "", &callError{text}
	}
	return duration.ReplaceAllString(strings.TrimSuffix(text, "\n"), "<ms>"), nil
}

// hostRewrite is a RoundTripper that sends every request with its Host header set to Host.
type hostRewrite struct{ Host string }

func (h hostRewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = h.Host
	return http.DefaultTransport.RoundTrip(r)
}

type callError struct{ text string }

func (e *callError) Error() string { return "tool error: " + e.text }

func streamable(port string, hc *http.Client) mcp.Transport {
	return &mcp.StreamableClientTransport{Endpoint: "http://127.0.0.1:" + port + "/mcp", HTTPClient: hc, DisableStandaloneSSE: true}
}

// inMemoryListSchemas returns the text of list_schemas over an in-memory transport, as stdio would get it.
func inMemoryListSchemas(t *testing.T, reg *server.Registry) string {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := reg.MCP().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	want, err := callListSchemas(ctx, ct)
	if err != nil {
		t.Fatal(err)
	}
	return want
}

func TestStreamableMatchesStdio(t *testing.T) {
	cfg, reg := newRegistry(t, testpg.RO(t), "--transport=streamable-http")
	schema.Register(reg)
	port := start(t, cfg, reg)

	got, err := callListSchemas(context.Background(), streamable(port, nil))
	if err != nil {
		t.Fatal(err)
	}
	if want := inMemoryListSchemas(t, reg); got != want {
		t.Errorf("HTTP text differs from in-memory text:\n%s\nvs\n%s", got, want)
	}
}

func TestSSEMatchesStdio(t *testing.T) {
	cfg, reg := newRegistry(t, testpg.RO(t), "--transport=sse")
	schema.Register(reg)
	port := start(t, cfg, reg)
	got, err := callListSchemas(context.Background(), &mcp.SSEClientTransport{Endpoint: "http://localhost:" + port + "/sse"})
	if err != nil {
		t.Fatal(err)
	}
	if want := inMemoryListSchemas(t, reg); got != want {
		t.Errorf("SSE text differs from in-memory text:\n%s\nvs\n%s", got, want)
	}
}

// The SDK's own Host check is off: an --allowed-hosts entry must work over a loopback connection.
func TestAllowedHostOverLoopback(t *testing.T) {
	for _, tr := range []string{"streamable-http", "sse"} {
		t.Run(tr, func(t *testing.T) {
			cfg, reg := newRegistry(t, testpg.RO(t), "--transport="+tr, "--allowed-hosts=db-mcp.internal")
			schema.Register(reg)
			port := start(t, cfg, reg)
			hc := &http.Client{Transport: hostRewrite{Host: "db-mcp.internal:" + port}}
			transport := streamable(port, hc)
			if tr == "sse" {
				transport = &mcp.SSEClientTransport{Endpoint: "http://127.0.0.1:" + port + "/sse", HTTPClient: hc}
			}
			got, err := callListSchemas(context.Background(), transport)
			if err != nil {
				t.Fatalf("list_schemas with Host db-mcp.internal: %v", err)
			}
			if want := inMemoryListSchemas(t, reg); got != want {
				t.Errorf("text differs from in-memory text:\n%s\nvs\n%s", got, want)
			}
		})
	}
}

func TestFiftyConcurrentCalls(t *testing.T) {
	// A temporary database keeps PgBouncer's server connections and late-closing sessions of other packages, which
	// carry the same application_name, out of the count.
	dbname := testpg.TempDB(t)
	cfg, reg := newRegistry(t, testpg.DSN(testpg.RO(t), dbname), "--transport=streamable-http")
	schema.Register(reg)
	port := start(t, cfg, reg)
	ctx := context.Background()

	admin, err := pgx.Connect(ctx, testpg.Admin(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	stop := make(chan struct{})
	peak := make(chan int, 1)
	go func() {
		most := 0
		for {
			var n int
			if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE application_name = 'postgres-dba-mcp' AND datname = $1", dbname).Scan(&n); err == nil && n > most {
				most = n
			}
			select {
			case <-stop:
				peak <- most
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()

	const n = 50
	texts := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	begin := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			texts[i], errs[i] = callListSchemas(ctx, streamable(port, nil))
		}()
	}
	close(begin)
	wg.Wait()
	close(stop)

	for i := range n {
		if errs[i] != nil {
			t.Errorf("call %d: %v", i, errs[i])
		} else if texts[i] != texts[0] {
			t.Errorf("call %d text differs from call 0", i)
		}
	}
	if most := <-peak; most > 5 {
		t.Errorf("%d sessions of postgres-dba-mcp at once, pool maximum is 5", most)
	}
}
