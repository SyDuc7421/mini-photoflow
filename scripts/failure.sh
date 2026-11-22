#!/usr/bin/env bash
# Stops one microservice and shows what still works: login, post, feed.
# Usage: scripts/failure.sh [service]   (default: social)
# Needs the microservices stack running and answering on BASE_URL (default http://localhost:8080).
# The feed mode is whatever the stack was started with (FEED_MODE=sync|event docker compose ... up).
set -euo pipefail

SERVICE="${1:-social}"
BASE_URL="${BASE_URL:-http://localhost:8080}"
cd "$(dirname "$0")/.."
compose() { docker compose -f microservices/docker-compose.yml "$@"; }

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
# 1x1 PNG
printf 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==' | base64 --decode > "$TMP/img.png"

code() { curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$@" || true; }
json() { sed -n "s/.*\"$1\":\"\{0,1\}\([^\",}]*\).*/\1/p"; }

suffix="$RANDOM$RANDOM"
# Kept in a variable: bash 3.2 (macOS /bin/bash) misparses escaped quotes inside `until`.
LOGIN_BODY="{\"username\":\"fa$suffix\",\"password\":\"pw123456\"}"
reg() { curl -s -X POST "$BASE_URL/register" -H 'Content-Type: application/json' -d "{\"username\":\"$1\",\"password\":\"pw123456\"}" || true; }
login() { curl -s -X POST "$BASE_URL/login" -H 'Content-Type: application/json' -d "{\"username\":\"$1\",\"password\":\"pw123456\"}" | json token || true; }

reg "fa$suffix" >/dev/null
BID="$(reg "fb$suffix" | json id)"
TA="$(login "fa$suffix")"
TB="$(login "fb$suffix")"
[ -n "$BID" ] && [ -n "$TA" ] && [ -n "$TB" ] || { echo "setup failed: could not register/login test users at $BASE_URL"; exit 1; }
code -X PUT "$BASE_URL/users/$BID/follow" -H "Authorization: Bearer $TA" >/dev/null
code -X POST "$BASE_URL/posts" -H "Authorization: Bearer $TB" -F caption=setup -F "image=@$TMP/img.png;type=image/png" >/dev/null

probe() { # label
  local l p f
  l="$(code -X POST "$BASE_URL/login" -H 'Content-Type: application/json' -d "$LOGIN_BODY")"
  p="$(code -X POST "$BASE_URL/posts" -H "Authorization: Bearer $TB" -F caption=probe -F "image=@$TMP/img.png;type=image/png")"
  f="$(code "$BASE_URL/feed" -H "Authorization: Bearer $TA")"
  printf '%-22s login=%s post=%s feed=%s\n' "$1" "$l" "$p" "$f"
}

echo "feed mode: $(compose exec -T feed printenv FEED_MODE 2>/dev/null || echo unknown)"
probe "all services up"
# From here on, always bring the service back, even on Ctrl-C or an error.
trap 'compose start "$SERVICE" >/dev/null 2>&1; rm -rf "$TMP"' EXIT INT TERM
compose stop "$SERVICE" >/dev/null 2>&1 || { echo "cannot stop service '$SERVICE'"; exit 1; }
sleep 2
probe "$SERVICE stopped"
compose start "$SERVICE" >/dev/null 2>&1
start=$SECONDS
until [ "$(code "$BASE_URL/feed" -H "Authorization: Bearer $TA")" = 200 ] \
   && [ "$(code -X POST "$BASE_URL/login" -H 'Content-Type: application/json' -d "$LOGIN_BODY")" = 200 ]; do
  [ $((SECONDS - start)) -lt 60 ] || { echo "did not recover within 60s"; exit 1; }
  sleep 1
done
echo "recovered after $((SECONDS - start))s"
