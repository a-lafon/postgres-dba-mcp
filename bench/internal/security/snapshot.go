// Package security replays the safesql attack corpus through each server and detects side effects by diffing
// snapshots of the database taken before and after every case.
package security

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Snapshot is the observable state of the fixture database.
type Snapshot struct {
	Relations     []string          // "schema.name:kind" of every relation in user schemas
	LargeObjects  int               // rows of pg_largeobject_metadata
	Sequences     []string          // "schema.name=last_value"
	RoleSettings  []string          // pg_db_role_setting rows
	Advisory      []string          // advisory locks held by any backend
	Tables        map[string]string // "schema.table" -> "<rows>/<hashtext sum>"
	SentinelAlive bool              // the sentinel session still exists
	Notifications []string          // notifications the sentinel received since the previous snapshot
	// NextXID is the next transaction id to be assigned and OpenXacts the other sessions holding one. They are not
	// compared: TakeAfter uses them to skip the table checksums when no transaction can have written.
	NextXID   string
	OpenXacts int
}

// CheckedTables are the tables whose content is checksummed.
var CheckedTables = []string{"public.accounts", "public.events", "public.orders"}

// Sentinel is a session that stays idle and listens on every channel the corpus names: a terminated session or a
// notification shows in the next snapshot.
type Sentinel struct {
	conn *pgx.Conn
	pid  int32
}

// NewSentinel connects the sentinel with dsn and makes it LISTEN on channels.
func NewSentinel(ctx context.Context, dsn string, channels []string) (*Sentinel, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect sentinel: %w", err)
	}
	for _, ch := range channels {
		if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{ch}.Sanitize()); err != nil {
			_ = conn.Close(ctx)
			return nil, fmt.Errorf("sentinel listen %q: %w", ch, err)
		}
	}
	var pid int32
	if err := conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("sentinel pid: %w", err)
	}
	return &Sentinel{conn: conn, pid: pid}, nil
}

// Close ends the sentinel session.
func (s *Sentinel) Close(ctx context.Context) { _ = s.conn.Close(ctx) }

// drain returns the notifications received since the last call.
func (s *Sentinel) drain(ctx context.Context) []string {
	var got []string
	for {
		wctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		n, err := s.conn.WaitForNotification(wctx)
		cancel()
		if err != nil {
			return got
		}
		got = append(got, n.Channel+":"+n.Payload)
	}
}

// Take reads a snapshot through the admin connection admin.
func Take(ctx context.Context, admin *pgx.Conn, s *Sentinel) (Snapshot, error) {
	return take(ctx, admin, s, nil)
}

// TakeAfter is Take, but it reuses the table checksums of base when no transaction id was assigned since base and no
// other session holds one: a committed write to a table needs a transaction id, so the tables cannot have changed.
// Checksumming the 5,000,000 rows of events takes seconds; the rest of the snapshot takes milliseconds.
func TakeAfter(ctx context.Context, admin *pgx.Conn, s *Sentinel, base Snapshot) (Snapshot, error) {
	return take(ctx, admin, s, &base)
}

func take(ctx context.Context, admin *pgx.Conn, s *Sentinel, base *Snapshot) (Snapshot, error) {
	var snap Snapshot
	// Read first: a write that commits while the snapshot is taken then shows as a moved id.
	if err := admin.QueryRow(ctx, `SELECT pg_snapshot_xmax(pg_current_snapshot())::text,
		(SELECT count(*) FROM pg_stat_activity WHERE backend_xid IS NOT NULL AND pid <> pg_backend_pid())`).Scan(&snap.NextXID, &snap.OpenXacts); err != nil {
		return snap, fmt.Errorf("snapshot transaction ids: %w", err)
	}
	strs := func(dst *[]string, q string) error {
		rows, err := admin.Query(ctx, q)
		if err != nil {
			return fmt.Errorf("snapshot %q: %w", q, err)
		}
		v, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("snapshot %q: %w", q, err)
		}
		sort.Strings(v)
		*dst = v
		return nil
	}
	queries := []struct {
		dst *[]string
		sql string
	}{
		// Relations with their column count and privileges, schemas and functions: CREATE, DROP, ALTER ... ADD COLUMN,
		// GRANT and CREATE FUNCTION all show.
		{&snap.Relations, `SELECT n.nspname || '.' || c.relname || ':' || c.relkind::text || ':' || c.relnatts || ':' || coalesce(c.relacl::text, '')
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast') AND n.nspname NOT LIKE 'pg\_temp%' AND n.nspname NOT LIKE 'pg\_toast\_temp%'
			UNION ALL SELECT 'schema ' || nspname || ':' || nspowner::regrole::text || ':' || coalesce(nspacl::text, '') FROM pg_namespace
			WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema'
			UNION ALL SELECT 'function ' || n.nspname || '.' || p.proname || '(' || pg_get_function_identity_arguments(p.oid) || ')'
			FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_temp%'`},
		{&snap.Sequences, `SELECT schemaname || '.' || sequencename || '=' || coalesce(last_value::text, 'null') FROM pg_sequences`},
		{&snap.RoleSettings, `SELECT setdatabase || '/' || setrole || '/' || setconfig::text FROM pg_db_role_setting`},
		{&snap.Advisory, `SELECT pid || '/' || classid || '/' || objid || '/' || mode FROM pg_locks WHERE locktype = 'advisory'`},
	}
	for _, q := range queries {
		if err := strs(q.dst, q.sql); err != nil {
			return snap, err
		}
	}
	if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_largeobject_metadata").Scan(&snap.LargeObjects); err != nil {
		return snap, fmt.Errorf("snapshot large objects: %w", err)
	}
	if base != nil && snap.NextXID == base.NextXID && snap.OpenXacts == 0 && base.Tables != nil {
		snap.Tables = base.Tables
	} else if err := checksum(ctx, admin, &snap); err != nil {
		return snap, err
	}
	if s != nil {
		var alive int
		if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE pid = $1", s.pid).Scan(&alive); err != nil {
			return snap, fmt.Errorf("snapshot sentinel: %w", err)
		}
		snap.SentinelAlive = alive == 1
		snap.Notifications = s.drain(ctx)
	}
	return snap, nil
}

// checksum reads the row count and content hash of CheckedTables.
func checksum(ctx context.Context, admin *pgx.Conn, snap *Snapshot) error {
	snap.Tables = map[string]string{}
	for _, t := range CheckedTables {
		var n, sum int64
		q := fmt.Sprintf("SELECT count(*), coalesce(sum(hashtext(t::text)), 0) FROM %s t", t)
		if err := admin.QueryRow(ctx, q).Scan(&n, &sum); err != nil {
			return fmt.Errorf("snapshot %s: %w", t, err)
		}
		snap.Tables[t] = fmt.Sprintf("%d/%d", n, sum)
	}
	return nil
}

// Diff describes what changed from a to b, one entry per change; it is empty when nothing did.
func Diff(a, b Snapshot) []string {
	var d []string
	d = append(d, setDiff("relations", a.Relations, b.Relations)...)
	if b.LargeObjects != a.LargeObjects {
		d = append(d, fmt.Sprintf("large objects %+d", b.LargeObjects-a.LargeObjects))
	}
	d = append(d, setDiff("sequences", a.Sequences, b.Sequences)...)
	d = append(d, setDiff("role settings", a.RoleSettings, b.RoleSettings)...)
	d = append(d, setDiff("advisory locks", a.Advisory, b.Advisory)...)
	keys := make([]string, 0, len(b.Tables))
	for k := range b.Tables {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if a.Tables[k] != b.Tables[k] {
			d = append(d, fmt.Sprintf("%s content %s -> %s", k, a.Tables[k], b.Tables[k]))
		}
	}
	if a.SentinelAlive && !b.SentinelAlive {
		d = append(d, "sentinel session terminated")
	}
	if len(b.Notifications) > 0 {
		d = append(d, fmt.Sprintf("notifications received: %s", strings.Join(b.Notifications, ", ")))
	}
	return d
}

func setDiff(name string, a, b []string) []string {
	in := func(v []string) map[string]bool {
		m := make(map[string]bool, len(v))
		for _, s := range v {
			m[s] = true
		}
		return m
	}
	ma, mb := in(a), in(b)
	var out []string
	for _, s := range b {
		if !ma[s] {
			out = append(out, name+" +"+s)
		}
	}
	for _, s := range a {
		if !mb[s] {
			out = append(out, name+" -"+s)
		}
	}
	return out
}

// ErrNoCases is returned when a corpus file holds no case.
var ErrNoCases = errors.New("no case in corpus")
