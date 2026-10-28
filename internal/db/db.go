// Package db is the only place that holds a connection to the database: a lazy, bounded pool and two
// transaction runners (read-only and read-write) that apply the session settings and clean up after every call.
package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
)

const (
	applicationName = "postgres-dba-mcp"
	maxConns        = 5
	maxConnIdleTime = 5 * time.Minute
	pingTimeout     = 5 * time.Second
	connectTimeout  = 10 * time.Second
	cleanupTimeout  = 5 * time.Second
)

// settingsSQL sets the session values for the current transaction only, so they hold behind any pooler.
// $1 is the statement timeout in milliseconds.
const settingsSQL = `SELECT set_config('statement_timeout', $1, true),
  set_config('lock_timeout', '10s', true),
  set_config('idle_in_transaction_session_timeout', '60s', true),
  set_config('TimeZone', 'UTC', true),
  set_config('DateStyle', 'ISO, YMD', true),
  set_config('IntervalStyle', 'postgres', true),
  set_config('bytea_output', 'hex', true)`

// searchPathSQL extends settingsSQL when the connection URI carried a search_path.
// $2 is that value; it is set per transaction, because PgBouncer before 1.21 refuses
// a search_path in the startup packet.
const searchPathSQL = `,
  set_config('search_path', $2, true)`

// logWriter receives the log lines of this package; tests replace it.
var (
	logMu     sync.Mutex
	logWriter io.Writer = os.Stderr
)

func logf(format string, args ...any) {
	logMu.Lock()
	defer logMu.Unlock()
	_, _ = fmt.Fprintf(logWriter, format+"\n", args...)
}

// ConnectError reports a failure to obtain a connection to the database.
type ConnectError struct{ Err error }

// Error returns "cannot connect to the database: " followed by the driver message, which holds no password.
func (e *ConnectError) Error() string { return "cannot connect to the database: " + e.Err.Error() }

// Unwrap returns the driver error.
func (e *ConnectError) Unwrap() error { return e.Err }

// Info describes the server: its version and its installed extensions.
type Info struct {
	VersionNum int
	Extensions map[string]string // extname -> extversion
}

// AtLeast reports whether an extension version of the form major.minor is at least major.minor; a version that
// cannot be read counts as older.
func AtLeast(version string, major, minor int) bool {
	ma, mi, ok := strings.Cut(version, ".")
	maj, err1 := strconv.Atoi(ma)
	mnr, err2 := strconv.Atoi(mi)
	return ok && err1 == nil && err2 == nil && (maj > major || maj == major && mnr >= minor)
}

// DB is the connection pool of the process and its transaction runners.
type DB struct {
	pool *pgxpool.Pool
	cfg  *config.Config
	// searchPath is what the connection URI asked for, empty when it asked for nothing.
	searchPath string

	mu        sync.Mutex
	info      *Info
	audit     auditState
	auditDone chan struct{} // closed once the role audit has completed
}

type auditState int

const (
	auditIdle auditState = iota
	auditRunning
	auditComplete
)

// poolConfig builds the pool configuration for cfg. It returns the search_path the connection URI
// asked for, if any, which begin applies per transaction.
func poolConfig(cfg *config.Config) (*pgxpool.Config, string, error) {
	pc, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, "", fmt.Errorf("parse database URI: %w", err)
	}
	pc.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec
	// Only application_name travels in the startup packet: PgBouncer refuses the timeouts there, and
	// refuses a search_path before 1.21. The user's search_path is kept and set per transaction.
	searchPath := pc.ConnConfig.RuntimeParams["search_path"]
	// Whatever the driver did not recognise as a connection option is in that same map, so dropping it
	// silently would hide a misspelling. A misspelled TLS option is the dangerous one: the connection
	// would fall back to sslmode=prefer, which encrypts without checking the server's certificate.
	for k := range pc.ConnConfig.RuntimeParams {
		switch {
		case k == "search_path" || k == "application_name":
		case strings.HasPrefix(k, "ssl"):
			return nil, "", fmt.Errorf("database URI: %q is not a connection option; the TLS options are sslmode, sslrootcert, sslcert, sslkey and sslpassword, and a misspelled one would leave the server's certificate unchecked", k)
		default:
			logf("database URI: %q is ignored: a server setting cannot travel in the connection, because a transaction pooler refuses it there", k)
		}
	}
	pc.ConnConfig.RuntimeParams = map[string]string{"application_name": applicationName}
	if pc.ConnConfig.ConnectTimeout == 0 {
		pc.ConnConfig.ConnectTimeout = connectTimeout
	}
	// Set after parsing so that pool_* DSN parameters cannot override them.
	pc.MaxConns = maxConns
	pc.MinConns = 0
	pc.MinIdleConns = 0
	pc.MaxConnIdleTime = maxConnIdleTime
	pc.PingTimeout = pingTimeout
	return pc, searchPath, nil
}

// New builds the pool. It opens no connection: the first one is opened by the first call.
func New(cfg *config.Config) (*DB, error) {
	pc, searchPath, err := poolConfig(cfg)
	if err != nil {
		return nil, err
	}
	d := &DB{cfg: cfg, searchPath: searchPath, auditDone: make(chan struct{})}
	pc.AfterRelease = d.afterRelease
	d.pool, err = pgxpool.NewWithConfig(context.Background(), pc)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	return d, nil
}

// Close closes the pool, waiting for the connections in use to be released.
func (d *DB) Close() { d.pool.Close() }

// afterRelease removes the session residue (hypothetical indexes, advisory locks, prepared statements, settings)
// before a connection goes back to the pool. pgxpool gives the hook no context, hence its own timeout.
func (d *DB) afterRelease(conn *pgx.Conn) bool {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	hypopg, err := d.hasHypopg(ctx, conn)
	if err != nil {
		return false
	}
	if hypopg && !d.resetHypopg(ctx, conn) {
		return false
	}
	_, err = conn.Exec(ctx, "DISCARD ALL")
	return err == nil
}

// resetHypopg drops the hypothetical indexes of conn. It runs in its own transaction, because hypopg
// may be reachable only through the URI's search_path, which only begin applies: a session-level setting
// would stay behind on one server connection of a transaction pooler and reach another client.
func (d *DB) resetHypopg(ctx context.Context, conn *pgx.Conn) bool {
	err := pgx.BeginFunc(ctx, conn, func(t pgx.Tx) error {
		if d.searchPath != "" {
			if _, err := t.Exec(ctx, "SELECT set_config('search_path', $1, true)", d.searchPath); err != nil {
				return err
			}
		}
		_, err := t.Exec(ctx, "SELECT hypopg_reset()")
		return err
	})
	return err == nil
}

func (d *DB) hasHypopg(ctx context.Context, conn *pgx.Conn) (bool, error) {
	d.mu.Lock()
	info := d.info
	d.mu.Unlock()
	if info != nil {
		_, ok := info.Extensions["hypopg"]
		return ok, nil
	}
	var ok bool
	err := conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'hypopg')").Scan(&ok)
	return ok, err
}

// tx wraps pgx.Tx so that every statement travels on the extended protocol, which accepts a single statement.
// pgx's Exec without arguments would otherwise use the simple protocol, which runs several.
type tx struct{ pgx.Tx }

// Exec runs sql through Query, drains the rows and returns the command tag.
func (t tx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	rows, err := t.Query(ctx, sql, args...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	rows.Close() // drains the remaining rows, which fixes the command tag
	return rows.CommandTag(), rows.Err()
}

// Begin opens a savepoint, wrapped the same way.
func (t tx) Begin(ctx context.Context) (pgx.Tx, error) {
	sp, err := t.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return tx{sp}, nil
}

// ReadOnly runs fn in a READ ONLY transaction that is always rolled back.
func (d *DB) ReadOnly(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return d.run(ctx, true, true, fn)
}

// ReadWrite runs fn in a transaction that is committed when fn succeeds and rolled back otherwise.
// Tools call it only in unrestricted mode.
func (d *DB) ReadWrite(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return d.run(ctx, false, true, fn)
}

func (d *DB) run(ctx context.Context, readOnly, audit bool, fn func(context.Context, pgx.Tx) error) error {
	for attempt := 0; ; attempt++ {
		c, err := d.pool.Acquire(ctx)
		if err != nil {
			if attempt == 0 && retryable(ctx, err) {
				continue
			}
			return &ConnectError{Err: err}
		}
		if audit {
			d.startAudit()
		}
		t, err := d.begin(ctx, c, readOnly)
		if err != nil {
			// A connection pgx has closed under us (backend killed) counts as a connection error too.
			dead := c.Conn().IsClosed() && ctx.Err() == nil
			discard(ctx, c)
			if attempt == 0 && (dead || retryable(ctx, err)) {
				continue
			}
			return err
		}
		return d.finish(ctx, c, t, readOnly, fn)
	}
}

// begin opens the transaction and applies the session settings; nothing of the caller's function has run yet.
func (d *DB) begin(ctx context.Context, c *pgxpool.Conn, readOnly bool) (tx, error) {
	opts := pgx.TxOptions{}
	if readOnly {
		opts.AccessMode = pgx.ReadOnly
	}
	pt, err := c.BeginTx(ctx, opts)
	if err != nil {
		return tx{}, err
	}
	t := tx{pt}
	ms := max(1, int64(math.Ceil(float64(d.cfg.StatementTimeout)/float64(time.Millisecond))))
	sql, args := settingsSQL, []any{strconv.FormatInt(ms, 10)}
	if d.searchPath != "" {
		sql, args = sql+searchPathSQL, append(args, d.searchPath)
	}
	if _, err := t.Exec(ctx, sql, args...); err != nil {
		_ = pt.Rollback(context.WithoutCancel(ctx))
		return tx{}, err
	}
	return t, nil
}

func (d *DB) finish(ctx context.Context, c *pgxpool.Conn, t tx, readOnly bool, fn func(context.Context, pgx.Tx) error) (err error) {
	endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	var endErr error
	defer func() {
		// A transaction that cannot be ended leaves the connection unusable: close it.
		if endErr != nil {
			discard(endCtx, c)
			return
		}
		c.Release()
	}()
	defer func() {
		if r := recover(); r != nil {
			_ = t.Rollback(endCtx)
			panic(r)
		}
	}()

	err = fn(ctx, t)
	if err == nil && !readOnly {
		if cerr := t.Commit(endCtx); cerr != nil {
			endErr = cerr
			return fmt.Errorf("commit: %w", cerr)
		}
		return nil
	}
	endErr = t.Rollback(endCtx)
	return err
}

// discard closes the connection so that the pool destroys it on release.
func discard(ctx context.Context, c *pgxpool.Conn) {
	_ = c.Conn().Close(context.WithoutCancel(ctx))
	c.Release()
}

// retryable reports whether err, raised before any statement of the caller ran, deserves one more attempt.
// A timeout is not retried: a second attempt would double the wait.
func retryable(ctx context.Context, err error) bool {
	return ctx.Err() == nil && isConnError(err)
}

// isConnError reports whether err is a connection error: SQLSTATE class 08, 57P01 to 57P03, or a network error.
func isConnError(err error) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		c := pe.Code
		return strings.HasPrefix(c, "08") || c == "57P01" || c == "57P02" || c == "57P03"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && !ne.Timeout()
}

// Info returns the server version and installed extensions, fetched on first use and cached on success.
func (d *DB) Info(ctx context.Context) (Info, error) {
	d.mu.Lock()
	cached := d.info
	d.mu.Unlock()
	if cached != nil {
		return *cached, nil
	}
	info := Info{Extensions: map[string]string{}}
	err := d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var v string
		if err := tx.QueryRow(ctx, "SELECT current_setting('server_version_num')").Scan(&v); err != nil {
			return err
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("parse server_version_num %q: %w", v, err)
		}
		info.VersionNum = n
		rows, err := tx.Query(ctx, "SELECT extname, extversion FROM pg_extension")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name, version string
			if err := rows.Scan(&name, &version); err != nil {
				return err
			}
			info.Extensions[name] = version
		}
		return rows.Err()
	})
	if err != nil {
		return Info{}, fmt.Errorf("load server info: %w", err)
	}
	d.mu.Lock()
	d.info = &info
	d.mu.Unlock()
	return info, nil
}
