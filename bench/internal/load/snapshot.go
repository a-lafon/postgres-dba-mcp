package load

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// SnapshotVolume is the Docker volume that holds the snapshot of the pg service's data folder. It is not in
// compose.yml, so `down -v` keeps it; the next `load` overwrites it.
const SnapshotVolume = "bench_pgsnap"

// dataDir is PGDATA of the pg service, on its volume.
const dataDir = "/var/lib/postgresql/data"

// Snapshot copies the data folder of the stopped pg service to SnapshotVolume. load takes it as its last step.
//
// The copy is of the whole cluster after a clean shutdown, so Restore brings back more than the rows: the cumulative
// statistics (dead tuples, index scans) and pg_stat_statements, which PostgreSQL writes to the data folder when it
// stops and which the health, index and workload tasks read. A database copied with CREATE DATABASE ... TEMPLATE
// would start with empty statistics, and pg_stat_statements is not per database.
func Snapshot(ctx context.Context, benchDir string, log io.Writer) error {
	return coldCopy(ctx, benchDir, log, dataDir, "/snap")
}

// Restore puts the snapshot back in place of the data folder of the pg service, and starts it again: every agent run
// starts from the database `load` left (PROTOCOL.md §4.3). It stops pg, so every connection to it is cut. It fails,
// and leaves the data folder as it was, when there is no snapshot.
func Restore(ctx context.Context, benchDir string, log io.Writer) error {
	return coldCopy(ctx, benchDir, log, "/snap", dataDir)
}

// coldCopy stops pg, replaces the content of the folder to by that of from, and starts pg again, whatever the copy
// did. Both folders are mounted in a container of the pg service's own image, which needs no pull.
func coldCopy(ctx context.Context, benchDir string, log io.Writer, from, to string) error {
	compose := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"compose", "-f", filepath.Join(benchDir, "compose.yml")}, args...)...)
		cmd.Stdout, cmd.Stderr = io.Discard, log
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("docker compose %s: %w", strings.Join(args, " "), err)
		}
		return nil
	}
	// The image's stop signal is SIGINT, a fast shutdown: sessions are cut and the statistics are written out.
	if err := compose("stop", "pg"); err != nil {
		return err
	}
	// The source must be a data folder, or the target is left untouched.
	const script = `test -f "$1/PG_VERSION" || { echo "no PostgreSQL data folder in $1" >&2; exit 3; }; ` +
		`find "$2" -mindepth 1 -delete && cp -a "$1/." "$2/"`
	err := compose("run", "--rm", "--no-deps", "-T", "-v", SnapshotVolume+":/snap", "--entrypoint", "sh", "pg", "-c", script, "sh", from, to)
	if startErr := compose("up", "-d", "--wait", "pg"); err == nil {
		err = startErr
	}
	return err
}
