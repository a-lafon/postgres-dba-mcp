package security

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

// Case is one entry of a safesql corpus.
type Case struct {
	ID       string
	Category string
	SQL      string
}

const caseMarker = "-- case: "

// ParseCorpus reads a corpus: `-- case: <id> <category>` lines, each followed by the SQL of the case.
func ParseCorpus(text string) ([]Case, error) {
	var cases []Case
	var cur *Case
	var sql []string
	flush := func() {
		if cur != nil {
			cur.SQL = strings.TrimSpace(strings.Join(sql, "\n"))
			cases = append(cases, *cur)
		}
		sql = nil
	}
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, caseMarker); ok {
			flush()
			f := strings.Fields(rest)
			if len(f) != 2 {
				return nil, fmt.Errorf("corpus line %q: want `-- case: <id> <category>`", line)
			}
			cur = &Case{ID: f[0], Category: f[1]}
			continue
		}
		sql = append(sql, line)
	}
	flush()
	if len(cases) == 0 {
		return nil, ErrNoCases
	}
	return cases, nil
}

var reChannel = regexp.MustCompile(`(?i)pg_notify\('([^']+)'|\bNOTIFY\s+"?(\w+)`)

// Channels returns the notification channels the cases name.
func Channels(cases []Case) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range cases {
		for _, m := range reChannel.FindAllStringSubmatch(c.SQL, -1) {
			ch := m[1] + m[2]
			if !seen[ch] {
				seen[ch] = true
				out = append(out, ch)
			}
		}
	}
	return out
}

// Replayer replays corpora against the fixture service of bench/compose.yml.
type Replayer struct {
	Compose  string // path of bench/compose.yml
	AdminDSN string
	Channels []string
	Log      io.Writer

	admin    *pgx.Conn
	sentinel *Sentinel
}

// Open connects the admin connection and the sentinel.
func (r *Replayer) Open(ctx context.Context) error {
	r.Close(ctx)
	admin, err := pgx.Connect(ctx, r.AdminDSN)
	if err != nil {
		return fmt.Errorf("connect admin: %w", err)
	}
	s, err := NewSentinel(ctx, r.AdminDSN, r.Channels)
	if err != nil {
		_ = admin.Close(ctx)
		return err
	}
	r.admin, r.sentinel = admin, s
	return nil
}

// Close drops the admin and sentinel connections.
func (r *Replayer) Close(ctx context.Context) {
	if r.sentinel != nil {
		r.sentinel.Close(ctx)
	}
	if r.admin != nil {
		_ = r.admin.Close(ctx)
	}
	r.admin, r.sentinel = nil, nil
}

// Recreate drops the fixture service with its volume and starts a fresh one, then reconnects.
func (r *Replayer) Recreate(ctx context.Context) error {
	r.Close(ctx)
	for _, args := range [][]string{{"rm", "-sfv", "fixture"}, {"up", "-d", "--wait", "fixture"}} {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"compose", "-f", r.Compose}, args...)...)
		cmd.Stderr, cmd.Stdout = os.Stderr, io.Discard
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker compose %s: %w", strings.Join(args, " "), err)
		}
	}
	return r.Open(ctx)
}

// Replay sends every case through execute_sql of the server and classifies the answer and the side effects. A side
// effect recreates the fixture and restarts the server before the next case. With sideEffects false, the snapshot
// diff is skipped (the legit corpus only counts refusals).
func (r *Replayer) Replay(ctx context.Context, spec servers.Spec, cases []Case, sideEffects bool) ([]results.SecurityCase, error) {
	var srv *servers.Server
	defer func() {
		if srv != nil {
			srv.Close()
		}
	}()
	var out []results.SecurityCase
	for _, c := range cases {
		if srv == nil {
			var err error
			if srv, err = spec.Start(ctx); err != nil {
				return nil, err
			}
		}
		var before Snapshot
		if sideEffects {
			var err error
			if before, err = Take(ctx, r.admin, r.sentinel); err != nil {
				return nil, fmt.Errorf("before %s: %w", c.ID, err)
			}
		}
		_, _ = fmt.Fprintf(r.Log, "security %s %s\n", spec.Name, c.ID)
		sc := results.SecurityCase{ID: c.ID, Category: c.Category}
		res, err := srv.Call(ctx, "execute_sql", map[string]any{"sql": c.SQL})
		restart := false
		if err != nil {
			sc.Class, sc.Text, restart = results.ClassFailed, "transport: "+err.Error(), true
		} else {
			sc.Class, sc.Text = servers.Classify(spec.Name, res.IsError, res.Text), firstLine(res.Text)
		}
		if sideEffects {
			after, err := Take(ctx, r.admin, r.sentinel)
			if err != nil {
				return nil, fmt.Errorf("after %s: %w", c.ID, err)
			}
			if d := Diff(before, after); len(d) > 0 {
				sc.SideEffect = strings.Join(d, "; ")
				if err := r.Recreate(ctx); err != nil {
					return nil, err
				}
				restart = true
			}
		}
		if restart {
			srv.Close()
			srv = nil
		}
		out = append(out, sc)
	}
	return out, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
