package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
)

// captureLog replaces the log writer for the test and returns what was written.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	logMu.Lock()
	old := logWriter
	logWriter = writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	})
	logMu.Unlock()
	t.Cleanup(func() {
		logMu.Lock()
		logWriter = old
		logMu.Unlock()
	})
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func testCfg(t testing.TB, dsn string, flags ...string) *config.Config {
	t.Helper()
	cfg, err := config.Parse(append(flags, dsn), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPoolConfig(t *testing.T) {
	tests := []struct {
		name        string
		dsn         string
		wantTimeout time.Duration
	}{
		{"default connect timeout", "postgres://u@h/db", 10 * time.Second},
		{"URI connect_timeout kept", "postgres://u@h/db?connect_timeout=3", 3 * time.Second},
		{"pool parameters overridden", "postgres://u@h/db?pool_max_conns=50&pool_min_conns=10&pool_max_conn_idle_time=1h", 10 * time.Second},
		{"startup parameters dropped", "postgres://u@h/db?statement_timeout=1&application_name=x", 10 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pc, _, err := poolConfig(testCfg(t, tt.dsn))
			if err != nil {
				t.Fatal(err)
			}
			if pc.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeExec {
				t.Errorf("exec mode = %v, want QueryExecModeExec", pc.ConnConfig.DefaultQueryExecMode)
			}
			if got := pc.ConnConfig.RuntimeParams; len(got) != 1 || got["application_name"] != "postgres-dba-mcp" {
				t.Errorf("RuntimeParams = %v, want only application_name=postgres-dba-mcp", got)
			}
			if pc.MaxConns != 5 || pc.MinConns != 0 || pc.MinIdleConns != 0 {
				t.Errorf("conns max/min/minIdle = %d/%d/%d, want 5/0/0", pc.MaxConns, pc.MinConns, pc.MinIdleConns)
			}
			if pc.MaxConnIdleTime != 5*time.Minute {
				t.Errorf("MaxConnIdleTime = %v, want 5m", pc.MaxConnIdleTime)
			}
			if pc.PingTimeout != 5*time.Second {
				t.Errorf("PingTimeout = %v, want 5s", pc.PingTimeout)
			}
			if pc.ConnConfig.ConnectTimeout != tt.wantTimeout {
				t.Errorf("ConnectTimeout = %v, want %v", pc.ConnConfig.ConnectTimeout, tt.wantTimeout)
			}
		})
	}
}

func TestNewOpensNoConnection(t *testing.T) {
	d, err := New(testCfg(t, "postgres://u@127.0.0.1:1/db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if n := d.pool.Stat().TotalConns(); n != 0 {
		t.Errorf("TotalConns = %d, want 0", n)
	}
}

func TestNewInvalidDSNDoesNotLeakPassword(t *testing.T) {
	_, err := New(testCfg(t, "postgres://u:s3cret@h:notaport/db"))
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("New() error = %v", err)
	}
}

func TestIsConnError(t *testing.T) {
	pg := func(code string) error { return &pgconn.PgError{Code: code} }
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"08000", pg("08000"), true},
		{"08006", pg("08006"), true},
		{"57P01", pg("57P01"), true},
		{"57P02", pg("57P02"), true},
		{"57P03", pg("57P03"), true},
		{"wrapped 57P01", fmt.Errorf("x: %w", pg("57P01")), true},
		{"network reset", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, true},
		{"EOF", io.ErrUnexpectedEOF, true},
		{"57014 statement timeout", pg("57014"), false},
		{"25006 read only", pg("25006"), false},
		{"42601 syntax", pg("42601"), false},
		{"28P01 auth", pg("28P01"), false},
		{"plain error", errors.New("boom"), false},
		{"deadline", context.DeadlineExceeded, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isConnError(tt.err); got != tt.want {
				t.Errorf("isConnError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRetryableSkipsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if retryable(ctx, &net.OpError{Err: syscall.ECONNRESET}) {
		t.Error("retryable() = true for a canceled context")
	}
}

func TestConnectErrorUnwrap(t *testing.T) {
	inner := &pgconn.PgError{Code: "28P01", Message: "password authentication failed"}
	err := fmt.Errorf("list: %w", &ConnectError{Err: inner})
	var ce *ConnectError
	var pe *pgconn.PgError
	if !errors.As(err, &ce) || !errors.As(err, &pe) || pe.Code != "28P01" {
		t.Errorf("errors.As failed on %v", err)
	}
	if !strings.HasPrefix(ce.Error(), "cannot connect to the database: ") {
		t.Errorf("Error() = %q", ce.Error())
	}
}

func TestVersionWarning(t *testing.T) {
	if w := versionWarning(newestTestedMajor*10000 + 4); w != "" {
		t.Errorf("tested major: got %q, want none", w)
	}
	want := fmt.Sprintf("WARNING: PostgreSQL %d is newer than %d", newestTestedMajor+1, newestTestedMajor)
	if w := versionWarning((newestTestedMajor + 1) * 10000); !strings.HasPrefix(w, want) {
		t.Errorf("got %q, want it to start with %q", w, want)
	}
}

// The denylist was checked against the majors of the CI matrix: newestTestedMajor must follow its newest one.
func TestNewestTestedMajorMatchesCI(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`pg_major: \[([0-9, ]+)\]`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no pg_major matrix in ci.yml")
	}
	majors := strings.Split(string(m[1]), ",")
	if last := strings.TrimSpace(majors[len(majors)-1]); last != strconv.Itoa(newestTestedMajor) {
		t.Errorf("ci.yml tests up to PostgreSQL %s, newestTestedMajor is %d", last, newestTestedMajor)
	}
}

// Everything the driver did not recognise as a connection option lands in RuntimeParams, which
// poolConfig throws away. A misspelled TLS option must not go with it in silence: the connection would
// fall back to sslmode=prefer, which encrypts without checking the server's certificate.
func TestPoolConfigRefusesAMisspelledTLSOption(t *testing.T) {
	_, _, err := poolConfig(testCfg(t, "postgres://u@h/db?sslmod=verify-full"))
	if err == nil || !strings.Contains(err.Error(), "sslmod") {
		t.Fatalf("error %v, want one naming sslmod", err)
	}
	// Spelled right, it never reaches that map: the driver turns it into a TLS configuration.
	pc, _, err := poolConfig(testCfg(t, "postgres://u@h/db?sslmode=verify-full"))
	if err != nil {
		t.Fatal(err)
	}
	if pc.ConnConfig.TLSConfig == nil || pc.ConnConfig.TLSConfig.InsecureSkipVerify {
		t.Error("sslmode=verify-full did not ask for a checked certificate")
	}
}

// A server setting cannot travel in the connection, but dropping it without a word leaves the user
// thinking it applied.
func TestPoolConfigWarnsAboutADroppedSetting(t *testing.T) {
	logged := captureLog(t)
	if _, _, err := poolConfig(testCfg(t, "postgres://u@h/db?timezone=UTC")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged(), `"timezone" is ignored`) {
		t.Errorf("the dropped setting was not reported: %q", logged())
	}
}

// A search_path in the URI is the user's, not a startup parameter to drop: it leaves RuntimeParams,
// which PgBouncer before 1.21 refuses, and comes back as the value begin sets per transaction.
func TestPoolConfigKeepsTheSearchPath(t *testing.T) {
	tests := []struct {
		name, dsn, want string
	}{
		{"none", "postgres://u@h/db", ""},
		{"from the URI", "postgres://u@h/db?search_path=audit,public", "audit,public"},
		{"with other parameters", "postgres://u@h/db?search_path=app&statement_timeout=1", "app"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pc, got, err := poolConfig(testCfg(t, tt.dsn))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("search path = %q, want %q", got, tt.want)
			}
			if _, ok := pc.ConnConfig.RuntimeParams["search_path"]; ok {
				t.Error("search_path stayed in the startup packet")
			}
		})
	}
}
