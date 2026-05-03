#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

NETWORK=proxy-test-net
BACKEND_A=test-backend-a
BACKEND_B=test-backend-b
PROXY=test-proxy
TOKEN=test-secret-token
CERT_DIR=$(mktemp -d)
PROXY_PORT=18443
PASS=0

RED='\033[0;31m'
GREEN='\033[0;32m'
NC='\033[0m'

cleanup() {
    echo ""
    echo "=== Cleanup ==="
    docker rm -f "$BACKEND_A" "$BACKEND_B" "$PROXY" 2>/dev/null || true
    docker network rm "$NETWORK" 2>/dev/null || true
    rm -rf "$CERT_DIR"
}
trap cleanup EXIT

assert_eq() {
    local label="$1" expected="$2" actual="$3"
    if [ "$actual" = "$expected" ]; then
        echo -e "${GREEN}PASS${NC} $label"
    else
        echo -e "${RED}FAIL${NC} $label"
        echo "  expected: $expected"
        echo "  actual:   $actual"
        PASS=1
    fi
}

BASE="https://localhost:${PROXY_PORT}"
AUTH="-H \"Authorization: Bearer $TOKEN\""

curl_proxy() {
    curl -sk "$@"
}

echo "=== Generating self-signed certificate for proxy ==="
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
    -keyout "$CERT_DIR/key.pem" -out "$CERT_DIR/cert.pem" \
    -days 1 -nodes -subj "/CN=test-proxy" 2>/dev/null

echo "=== Building images ==="
docker build -t https-dynamic-proxy:test "$PROJECT_DIR" -q
docker build -t test-backend:test "$SCRIPT_DIR/backend/" -q

echo "=== Creating network ==="
docker network create "$NETWORK" 2>/dev/null || true

echo "=== Starting backends ==="
docker run -d --name "$BACKEND_A" --network "$NETWORK" test-backend:test
docker run -d --name "$BACKEND_B" --network "$NETWORK" test-backend:test

echo "=== Starting proxy ==="
docker run -d --name "$PROXY" --network "$NETWORK" \
    -p "${PROXY_PORT}:443" \
    -v "$CERT_DIR:/certs:ro" \
    -e PROXY_DOMAIN=test-proxy \
    -e REGISTER_TOKEN="$TOKEN" \
    -e BACKEND_TLS_VERIFY=false \
    -e DEV_TLS_CERT=/certs/cert.pem \
    -e DEV_TLS_KEY=/certs/key.pem \
    -e DATA_DIR=/tmp/data \
    https-dynamic-proxy:test

echo "=== Waiting for proxy to be ready ==="
for i in $(seq 1 10); do
    if curl -sk "${BASE}/" >/dev/null 2>&1; then break; fi
    sleep 1
done

echo ""
echo "=== Test 1: バックエンド未登録時に 503 ==="
STATUS=$(curl_proxy -o /dev/null -w "%{http_code}" "${BASE}/")
assert_eq "503" "503" "$STATUS"

echo ""
echo "=== Test 2: 不正トークンで 401 ==="
STATUS=$(curl_proxy -o /dev/null -w "%{http_code}" -X POST "${BASE}/backends" \
    -H "Authorization: Bearer wrong" \
    -H "Content-Type: application/json" \
    -d '{"fqdn":"x.example.com"}')
assert_eq "401" "401" "$STATUS"

echo ""
echo "=== Test 3: デフォルトバックエンドを登録 ==="
STATUS=$(curl_proxy -o /dev/null -w "%{http_code}" -X POST "${BASE}/backends" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"fqdn\":\"${BACKEND_A}\"}")
assert_eq "登録 200" "200" "$STATUS"

echo ""
echo "=== Test 4: デフォルトバックエンドへのHostヘッダ書き換え ==="
RESP=$(curl_proxy "${BASE}/")
HOST=$(echo "$RESP" | grep -o '"host":"[^"]*"' | cut -d'"' -f4)
assert_eq "Host → backend-a" "$BACKEND_A" "$HOST"

echo ""
echo "=== Test 5: 名前付きバックエンドを登録 ==="
STATUS=$(curl_proxy -o /dev/null -w "%{http_code}" -X POST "${BASE}/backends" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"name\":\"backend-b\",\"fqdn\":\"${BACKEND_B}\"}")
assert_eq "登録 200" "200" "$STATUS"

echo ""
echo "=== Test 6: 名前付きバックエンドへのルーティングとHostヘッダ書き換え ==="
RESP=$(curl_proxy "${BASE}/backend-b/")
HOST=$(echo "$RESP" | grep -o '"host":"[^"]*"' | cut -d'"' -f4)
assert_eq "Host → backend-b" "$BACKEND_B" "$HOST"

echo ""
echo "=== Test 7: 名前付きプレフィックスが取り除かれてパスが転送される ==="
RESP=$(curl_proxy "${BASE}/backend-b/some/path")
PATH_VAL=$(echo "$RESP" | grep -o '"path":"[^"]*"' | cut -d'"' -f4)
assert_eq "パス /some/path" "/some/path" "$PATH_VAL"

echo ""
echo "=== Test 8: 一致しないパスはデフォルトバックエンドへ ==="
RESP=$(curl_proxy "${BASE}/not-registered/path")
HOST=$(echo "$RESP" | grep -o '"host":"[^"]*"' | cut -d'"' -f4)
assert_eq "Host → backend-a (default)" "$BACKEND_A" "$HOST"

echo ""
echo "=== Test 9: バックエンド一覧 ==="
RESP=$(curl_proxy "${BASE}/backends" -H "Authorization: Bearer $TOKEN")
assert_eq "backend-a in default" "true" "$(echo "$RESP" | grep -q "$BACKEND_A" && echo true || echo false)"
assert_eq "backend-b in named"   "true" "$(echo "$RESP" | grep -q "$BACKEND_B" && echo true || echo false)"

echo ""
echo "=== Test 10: 名前付きバックエンドを削除 ==="
STATUS=$(curl_proxy -o /dev/null -w "%{http_code}" -X DELETE "${BASE}/backends/backend-b" \
    -H "Authorization: Bearer $TOKEN")
assert_eq "削除 200" "200" "$STATUS"
RESP=$(curl_proxy "${BASE}/backend-b/")
HOST=$(echo "$RESP" | grep -o '"host":"[^"]*"' | cut -d'"' -f4)
assert_eq "削除後はデフォルトへ" "$BACKEND_A" "$HOST"

echo ""
echo "=== Test 11: 予約語は登録不可 ==="
STATUS=$(curl_proxy -o /dev/null -w "%{http_code}" -X POST "${BASE}/backends" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"name":"backends","fqdn":"x.example.com"}')
assert_eq "予約語 400" "400" "$STATUS"

echo ""
if [ $PASS -eq 0 ]; then
    echo -e "${GREEN}すべてのテストが通過しました${NC}"
else
    echo -e "${RED}テストが失敗しました${NC}"
    exit 1
fi
