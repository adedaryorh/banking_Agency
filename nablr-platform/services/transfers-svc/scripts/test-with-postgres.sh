#!/usr/bin/env bash


set -euo pipefail


SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SVC_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

locate_pg() {
  if command -v initdb >/dev/null 2>&1 && command -v pg_ctl >/dev/null 2>&1; then
    dirname "$(command -v initdb)"
    return 0
  fi
  local candidates=(
    /opt/homebrew/opt/postgresql@*/bin
    /usr/local/opt/postgresql@*/bin
    /opt/homebrew/opt/postgresql/bin
    /usr/local/opt/postgresql/bin
    /opt/homebrew/bin
    /usr/local/bin
    /Applications/Postgres.app/Contents/Versions/*/bin
    /usr/lib/postgresql/*/bin
  )
  local d
  for d in "${candidates[@]}"; do
    if [ -x "$d/initdb" ] && [ -x "$d/pg_ctl" ]; then
      echo "$d"
      return 0
    fi
  done
  return 1
}

if ! PG_BIN="$(locate_pg)"; then
  cat >&2 <<'EOF'
error: could not find PostgreSQL server binaries (initdb, pg_ctl, createdb).

Install one of:
  brew install postgresql@16          # Homebrew
  https://postgresapp.com             # Postgres.app

then re-run. (The client libpq alone is not enough — this needs the server.)
EOF
  exit 1
fi

INITDB="$PG_BIN/initdb"
PG_CTL="$PG_BIN/pg_ctl"
CREATEDB="$PG_BIN/createdb"
echo "using postgres toolchain: $PG_BIN ($("$PG_CTL" --version))"

# ---------------------------------------------------------------------------
# Stand up a disposable cluster
# ---------------------------------------------------------------------------

PORT="${PGPORT:-5433}"
DBNAME="transfers_test"

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/transfers-pg.XXXXXX")"
PGDATA="$WORK_DIR/data"
SOCK_DIR="$WORK_DIR/sock"        # short path: the socket filename has a length limit
mkdir -p "$SOCK_DIR"

STARTED=0
cleanup() {
  if [ "$STARTED" = "1" ] && [ "${KEEP_DB:-0}" != "1" ]; then
    "$PG_CTL" -D "$PGDATA" -s -m immediate stop >/dev/null 2>&1 || true
  fi
  if [ "${KEEP_DB:-0}" = "1" ]; then
    echo "KEEP_DB=1 — cluster left running at $PGDATA (socket $SOCK_DIR, port $PORT)"
    echo "stop it with: \"$PG_CTL\" -D \"$PGDATA\" stop && rm -rf \"$WORK_DIR\""
  else
    rm -rf "$WORK_DIR"
  fi
}
trap cleanup EXIT

echo "initializing throwaway cluster in $PGDATA"
# --auth=trust: local socket connections need no password, so user=postgres in
# the DSN just works. LC_ALL/LANG=C sidesteps locale-init failures on minimal
# shells. This cluster never survives the script, so trust is not a risk.
LC_ALL=C LANG=C "$INITDB" -D "$PGDATA" \
  --username=postgres --auth=trust --locale=C --encoding=UTF8 >/dev/null

# Socket-only, no TCP: nothing binds a network port, so this cannot collide with
# a Postgres you already run. fsync=off because a disposable test DB has nothing
# to lose to a crash and we would rather it be fast.
{
  echo "listen_addresses = ''"
  echo "unix_socket_directories = '$SOCK_DIR'"
  echo "port = $PORT"
  echo "fsync = off"
  echo "synchronous_commit = off"
  echo "full_page_writes = off"
} >> "$PGDATA/postgresql.conf"

echo "starting postgres"
"$PG_CTL" -D "$PGDATA" -l "$WORK_DIR/postgres.log" -w start >/dev/null
STARTED=1

"$CREATEDB" -h "$SOCK_DIR" -p "$PORT" -U postgres "$DBNAME"

# pgx keyword/value DSN over the unix socket (host is a directory path).
export TRANSFERS_TEST_DSN="host=$SOCK_DIR port=$PORT user=postgres dbname=$DBNAME sslmode=disable"
echo "TRANSFERS_TEST_DSN=$TRANSFERS_TEST_DSN"

# ---------------------------------------------------------------------------
# Run the tests
# ---------------------------------------------------------------------------

export GOCACHE="${GOCACHE:-$TMPDIR/gocache}"
export GOTMPDIR="${GOTMPDIR:-$TMPDIR}"

# Default target: the whole module, with the race detector, no test cache.
if [ "$#" -eq 0 ]; then
  set -- ./... -race -count=1
fi

echo
echo "==> go test $*"
echo
cd "$SVC_DIR"
go test "$@"
