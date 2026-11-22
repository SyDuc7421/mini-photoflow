#!/usr/bin/env bash
# Measures GET /feed latency on whatever stack answers at BASE_URL (monolith or microservices).
# Creates one viewer following 5 authors with 10 posts each (50 posts), waits until the first
# page is full (the event-driven feed is eventually consistent), then times N sequential requests.
# Each run leaves 6 users, 5 follows and 50 posts (with MinIO objects) behind.
# Usage: scripts/bench-feed.sh [N]      (default 200)
set -euo pipefail

N="${1:-200}"
case "$N" in ''|*[!0-9]*|0) echo "N must be a positive integer"; exit 1 ;; esac
BASE_URL="${BASE_URL:-http://localhost:8080}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
printf 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==' | base64 --decode > "$TMP/img.png"

suffix="$RANDOM$RANDOM"
json() { sed -n "s/.*\"$1\":\"\{0,1\}\([^\",}]*\).*/\1/p"; }
reg() { curl -s -X POST "$BASE_URL/register" -H 'Content-Type: application/json' -d "{\"username\":\"$1\",\"password\":\"pw123456\"}" || true; }
login() { curl -s -X POST "$BASE_URL/login" -H 'Content-Type: application/json' -d "{\"username\":\"$1\",\"password\":\"pw123456\"}" | json token || true; }
setup_failed() { echo "setup failed: could not register/login test users at $BASE_URL"; exit 1; }

reg "bv$suffix" >/dev/null
TV="$(login "bv$suffix")"
[ -n "$TV" ] || setup_failed
for a in 1 2 3 4 5; do
  id="$(reg "ba$a$suffix" | json id)"
  ta="$(login "ba$a$suffix")"
  [ -n "$id" ] && [ -n "$ta" ] || setup_failed
  curl -s -o /dev/null -X PUT "$BASE_URL/users/$id/follow" -H "Authorization: Bearer $TV"
  for p in 1 2 3 4 5 6 7 8 9 10; do
    curl -s -o /dev/null -X POST "$BASE_URL/posts" -H "Authorization: Bearer $ta" -F "caption=post $p" -F "image=@$TMP/img.png;type=image/png"
  done
done

feed() { curl -s "$BASE_URL/feed?limit=20" -H "Authorization: Bearer $TV"; }
for _ in $(seq 1 30); do
  [ "$(feed | grep -o '"id"' | wc -l | tr -d ' ')" = 20 ] && break
  sleep 0.2
done
[ "$(feed | grep -o '"id"' | wc -l | tr -d ' ')" = 20 ] || { echo "feed never returned 20 items"; exit 1; }

for _ in $(seq 1 "$N"); do
  curl -s -o /dev/null -w '%{http_code} %{time_total}\n' "$BASE_URL/feed?limit=20" -H "Authorization: Bearer $TV" || echo "000 0"
done > "$TMP/times"
bad="$(awk '$1 != 200' "$TMP/times" | wc -l | tr -d ' ')"
[ "$bad" = 0 ] || { echo "$bad of $N requests did not return 200"; exit 1; }
awk '{ print $2 }' "$TMP/times" | sort -n | awk -v n="$N" '
  function idx(p,  i) { i = int(n * p); if (i < n * p) i++; if (i < 1) i = 1; return i }  # nearest rank
  { v[NR] = $1 * 1000; s += $1 * 1000 }
  END { printf "GET /feed?limit=20, %d requests: p50=%.1fms p95=%.1fms mean=%.1fms max=%.1fms\n", n, v[idx(0.50)], v[idx(0.95)], s / NR, v[NR] }'
