#!/usr/bin/env bash
# Usage: scripts/smoke-initialize.sh <command...>
# Starts the command with DATABASE_URI pointing at an unreachable database, sends `initialize` and
# `notifications/initialized`, and requires an initialize result whose serverInfo.name is postgres-dba-mcp within
# 5 seconds. It then calls execute_sql with a denied function and a syntax error: the parser refuses both before
# any connection, so the artifact's parser is checked without a database. Exits 0 only if everything is answered.
# Stdin stays open until the responses are read: the server stops reading at end of file and drops replies in flight.
set -u

if [ "$#" -eq 0 ]; then
  echo "usage: $0 <command...>" >&2
  exit 2
fi

# Port 1 on loopback refuses at once; the server never connects before a tool call anyway.
# `docker run -e DATABASE_URI` forwards the variable to the container.
export DATABASE_URI='postgres://smoke:smoke@127.0.0.1:1/smoke?sslmode=disable'

dir=$(mktemp -d)
trap 'kill "${pid:-}" 2>/dev/null; rm -rf "$dir"' EXIT
mkfifo "$dir/in"
out="$dir/out"

"$@" <"$dir/in" >"$out" 2>"$dir/err" &
pid=$!

# Opening the fifo for writing blocks until the command opens it for reading.
exec 3>"$dir/in"
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' >&3
printf '%s\n' '{"jsonrpc":"2.0","method":"notifications/initialized"}' >&3

# wait_for <pattern>: poll stdout for up to 5 s; stdin (fd 3) stays open meanwhile.
wait_for() {
  for _ in $(seq 1 50); do
    grep -qF -- "$1" "$out" 2>/dev/null && return 0
    sleep 0.1
  done
  return 1
}

fail() {
  exec 3>&-
  echo "FAIL: $1 within 5 s" >&2
  echo "--- stdout" >&2
  cat "$out" >&2
  echo "--- stderr" >&2
  cat "$dir/err" >&2
  exit 1
}

wait_for '"name":"postgres-dba-mcp"' || fail "no initialize response with serverInfo.name=postgres-dba-mcp"

printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"execute_sql","arguments":{"sql":"SELECT pg_advisory_lock(1)"}}}' >&3
printf '%s\n' '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"execute_sql","arguments":{"sql":"SELEC 1"}}}' >&3
wait_for 'ERROR: restricted mode: function pg_advisory_lock() is not allowed' || fail "denied function not refused by the parser"
wait_for '(position 1)' || fail "syntax error reported without a position"

exec 3>&-
echo "ok: initialize answered, parser refuses a denied function and locates a syntax error"
