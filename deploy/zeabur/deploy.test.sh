#!/usr/bin/env bash
#
# Tests for deploy.sh. Stubs the `zeabur` CLI (no token / network needed) so we
# can lock in the two bugs the maiden v0.4.5 deploy exposed:
#   - Bug A: services must be resolved by --id (not --name) — verify update is
#            called with the id from `service list`.
#   - Bug B: the CLI exits 0 even when it prints ERROR — verify deploy.sh still
#            fails loudly instead of reporting a no-op as success.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
DEPLOY="$HERE/deploy.sh"
TMPROOT="$(mktemp -d)"
trap 'rm -rf "$TMPROOT"' EXIT

BIN="$TMPROOT/bin"
mkdir -p "$BIN"

# Single fake `zeabur`; behavior switches on $FAKE_MODE. Records every
# `service update tag` invocation to $FAKE_UPDATE_LOG so tests can assert how
# the service was identified.
cat > "$BIN/zeabur" <<'STUB'
#!/usr/bin/env bash
sub="${1:-}"; shift || true
case "$sub" in
  auth) exit 0 ;;
  service)
    action="${1:-}"; shift || true
    case "$action" in
      list)
        case "${FAKE_MODE:-ok}" in
          missing)   echo '[{"ID":"svc_other","Name":"something-else","Template":"PREBUILT"}]' ;;
          listerror) echo "ERROR	list environment services failed" ;; # note: still exits 0
          *)         echo '[{"ID":"svc_back","Name":"multica-backend","Template":"PREBUILT"},{"ID":"svc_web","Name":"multica-web","Template":"PREBUILT"}]' ;;
        esac
        exit 0 ;;
      update)
        echo "service update $*" >> "$FAKE_UPDATE_LOG"
        if [ "${FAKE_MODE:-ok}" = "updateerror" ]; then
          echo "ERROR	deploy backend failed"   # the Bug B trap: ERROR but exit 0
          exit 0
        fi
        echo "Service updated."
        exit 0 ;;
    esac ;;
esac
exit 0
STUB
chmod +x "$BIN/zeabur"

passes=0; fails=0
ok()  { echo "  PASS: $1"; passes=$((passes+1)); }
bad() { echo "  FAIL: $1"; fails=$((fails+1)); }

run() { # $1=mode ; runs deploy.sh, writes exit code to $RC, output to $OUT, update log to $LOG
  LOG="$TMPROOT/update_$1.log"; OUT="$TMPROOT/out_$1.log"; : > "$LOG"
  FAKE_MODE="$1" FAKE_UPDATE_LOG="$LOG" ENV_ID="env_test" TAG="v9.9.9" \
    PATH="$BIN:$PATH" bash "$DEPLOY" multica-backend multica-web >"$OUT" 2>&1
  RC=$?
}

echo "Test 1: happy path resolves ids and updates both services by --id"
run ok
if [ "$RC" -eq 0 ] \
   && grep -q -- '--id svc_back' "$LOG" && grep -q -- '--id svc_web' "$LOG" \
   && grep -q -- '--tag v9.9.9' "$LOG"; then
  ok "updated multica-backend (svc_back) and multica-web (svc_web) at v9.9.9 by --id"
else
  bad "rc=$RC; update log:\n$(cat "$LOG")\noutput:\n$(cat "$OUT")"
fi

echo "Test 2 (Bug B): update prints ERROR but exits 0 -> deploy must FAIL"
run updateerror
if [ "$RC" -ne 0 ]; then
  ok "deploy.sh failed loudly (rc=$RC) instead of reporting a no-op as success"
else
  bad "deploy.sh returned success despite an ERROR from the CLI (rc=0)"
fi

echo "Test 3 (Bug A): service not in env list -> deploy must FAIL (cannot resolve id)"
run missing
if [ "$RC" -ne 0 ] && grep -qi "could not resolve" "$OUT"; then
  ok "deploy.sh failed when a service id could not be resolved"
else
  bad "rc=$RC; expected an unresolved-id failure. output:\n$(cat "$OUT")"
fi

echo "Test 4: 'service list' prints ERROR but exits 0 -> deploy must FAIL"
run listerror
if [ "$RC" -ne 0 ]; then
  ok "deploy.sh failed loudly on a list-time error"
else
  bad "deploy.sh ignored an ERROR from 'service list' (rc=0)"
fi

echo
echo "deploy.test.sh: $passes passed, $fails failed"
[ "$fails" -eq 0 ]
