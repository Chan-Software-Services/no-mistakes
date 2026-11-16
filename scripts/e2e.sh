#!/usr/bin/env bash
# Suite wrapper for temporary E2E daemon ownership.
#
# Usage:
#   scripts/e2e.sh                  the whole suite in one go test process
#   scripts/e2e.sh --shard <i>/<n>  shard i of n, as CI's e2e shard jobs run it
#   scripts/e2e.sh <go test args>   go test with exactly these arguments
#
# A shard runs every n-th test of ./internal/e2e/... in name order (round-robin
# from the i-th, from `go test -list`), so each test runs in exactly one
# shard; shard 1 then runs the ./internal/pipeline/steps/... packages, in a
# second go test so the -run filter does not reach them. The e2e tests cannot
# use t.Parallel (each harness sets process-wide env with t.Setenv), so
# separate processes on separate runners are what makes them run side by side.
#
# Responsibilities:
#   1. Exact inventory directory (mode 0700) for this suite invocation
#   2. EXIT/INT/TERM trap that reaps only inventoried temp daemons
#   3. Concurrency cap via NM_E2E_DAEMON_MAX (default 2)
#   4. Pre-reap of any leftover inventory from a prior killed wrapper
#
# Honest boundary: this EXIT trap does NOT survive SIGKILL of this shell.
# When the wrapper itself is SIGKILL'd, the on-disk inventory is recovered
# on the next suite start (this script's pre-reap + package TestMain).
# Child go-test interruption/timeout/SIGKILL is covered: this shell still
# runs the trap and reaps.
set -u

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT" || exit 1

# The whole suite's cap is half again over CI's slowest whole-package run
# (477s before it was sharded). A shard's cap covers each of its go test
# processes: its share of ./internal/e2e, which is about a third of the
# package with three shards, and on shard 1 the steps packages.
WHOLE_TIMEOUT=720s
SHARD_TIMEOUT=480s
E2E_PKGS=./internal/e2e/...
STEPS_PKGS=./internal/pipeline/steps/...

usage() {
  printf 'usage: scripts/e2e.sh [--shard <i>/<n> | <go test args>...]\n' >&2
}

SHARD_INDEX=0
SHARD_COUNT=0
if [[ "${1:-}" == "--shard" ]]; then
  if [[ "$#" -ne 2 || ! "$2" =~ ^([0-9]+)/([0-9]+)$ ]]; then
    usage
    exit 2
  fi
  SHARD_INDEX=$((10#${BASH_REMATCH[1]}))
  SHARD_COUNT=$((10#${BASH_REMATCH[2]}))
  if ((SHARD_COUNT < 1 || SHARD_INDEX < 1 || SHARD_INDEX > SHARD_COUNT)); then
    usage
    exit 2
  fi
fi

if [[ -z "${NM_E2E_DAEMON_INVENTORY:-}" ]]; then
  base="/tmp"
  if [[ -d /private/tmp ]]; then
    base="/private/tmp"
  fi
  NM_E2E_DAEMON_INVENTORY_PARENT="${base}/no-mistakes-e2e-inventories-$(id -u)"
  if [[ -L "$NM_E2E_DAEMON_INVENTORY_PARENT" ]]; then
    exit 1
  fi
  mkdir -p "$NM_E2E_DAEMON_INVENTORY_PARENT" || exit 1
  chmod 700 "$NM_E2E_DAEMON_INVENTORY_PARENT" || exit 1
  NM_E2E_DAEMON_INVENTORY="$(mktemp -d "${NM_E2E_DAEMON_INVENTORY_PARENT}/run-XXXXXX")" || exit 1
  export NM_E2E_DAEMON_INVENTORY
  export NM_E2E_DAEMON_INVENTORY_PARENT
  chmod 700 "$NM_E2E_DAEMON_INVENTORY" || exit 1
  printf '%s\n' "$$" >"$NM_E2E_DAEMON_INVENTORY/owner.pid" || exit 1
  chmod 600 "$NM_E2E_DAEMON_INVENTORY/owner.pid" || exit 1
  OWNED_INVENTORY=1
else
  mkdir -p "$NM_E2E_DAEMON_INVENTORY"
  chmod 700 "$NM_E2E_DAEMON_INVENTORY" 2>/dev/null || true
  OWNED_INVENTORY=0
fi

export NM_E2E_DAEMON_MAX="${NM_E2E_DAEMON_MAX:-2}"

reap_inventory() {
  # Best-effort; never expand into shared-daemon territory (reaper refuses).
  (cd "$ROOT" && go run ./internal/e2edaemon/reapmain.go) >/dev/null 2>&1 || true
}

if [[ -n "${NM_E2E_DAEMON_INVENTORY_PARENT:-}" ]]; then
  export NM_E2E_REAP_ABANDONED=1
  reap_inventory
  unset NM_E2E_REAP_ABANDONED
fi

trap 'reap_inventory; if [[ "${OWNED_INVENTORY}" -eq 1 ]]; then rm -rf "$NM_E2E_DAEMON_INVENTORY" 2>/dev/null || true; fi' EXIT INT TERM

run_shard() {
  local list names selected code=0
  if ! list="$(go test -tags=e2e -list '.*' "$E2E_PKGS")"; then
    printf '%s\n' "$list" >&2
    return 1
  fi
  # Test names are the lines that are a bare identifier; each package's ok
  # line has spaces in it. A name two packages share is one entry, and its
  # shard's -run runs it in both.
  names="$(printf '%s\n' "$list" | grep -E '^[A-Za-z_][A-Za-z0-9_]*$' | LC_ALL=C sort -u)"
  selected="$(printf '%s\n' "$names" | awk -v n="$SHARD_COUNT" -v i="$SHARD_INDEX" 'NF && (NR - 1) % n == i - 1')"
  printf 'e2e.sh: shard %d/%d runs %d of the %d tests in %s\n' \
    "$SHARD_INDEX" "$SHARD_COUNT" "$(printf '%s' "$selected" | grep -c .)" "$(printf '%s' "$names" | grep -c .)" "$E2E_PKGS" >&2
  if [[ -n "$selected" ]]; then
    go test -tags=e2e -count=1 -timeout "$SHARD_TIMEOUT" -run "^($(printf '%s' "$selected" | paste -sd '|' -))\$" "$E2E_PKGS" || code=$?
  fi
  if ((SHARD_INDEX == 1)); then
    local steps=0
    go test -tags=e2e -count=1 -timeout "$SHARD_TIMEOUT" "$STEPS_PKGS" || steps=$?
    ((code != 0)) || code=$steps
  fi
  return "$code"
}

if ((SHARD_COUNT > 0)); then
  run_shard
  exit "$?"
fi

if [[ "$#" -eq 0 ]]; then
  set -- -tags=e2e -count=1 -timeout "$WHOLE_TIMEOUT" "$E2E_PKGS" "$STEPS_PKGS"
fi

go test "$@"
code=$?
exit "$code"
