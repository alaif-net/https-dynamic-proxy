#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

NETWORK=proxy-test-net
BACKEND=test-backend
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
    docker rm -f "$BACKEND" "$PROXY" 2>/dev/null || true
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

echo "=== Generating self-signed certificate for proxy ==="
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
    -keyout "$CERT_DIR/key.pem" -out "$CERT_DIR/cert.pem" \
    -days 1 -nodes -subj "/CN=test-proxy" 2>/dev/null

echo "=== Building images ==="
docker build -t https-dynamic-proxy:test "$PROJECT_DIR" -q
docker build -t test-backend:test "$SCRIPT_DIR/backend/" -q

echo "=== Creating network ==="
docker network create "$NETWORK" 2>/dev/null || true

echo "=== Starting backend ==="
docker run -d --name "$BACKEND" --network "$NETWORK" test-backend:test

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
    if curl -sk "https://localhost:${PROXY_PORT}/" >/dev/null 2>&1; then
        break
    fi
    sleep 1
done

echo ""
echo "=== Test 1: バックエンド未登録時に 503 を返すこと ==="
STATUS=$(curl -sk -o /dev/null -w "%{http_code}" "https://localhost:${PROXY_PORT}/")
assert_eq "未登録時 503" "503" "$STATUS"

echo ""
echo "=== Test 2: バックエンドを登録できること ==="
REG_STATUS=$(curl -sk -o /dev/null -w "%{http_code}" \
    -X POST "https://localhost:${PROXY_PORT}/register" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"fqdn\": \"$BACKEND\"}")
assert_eq "登録 200" "200" "$REG_STATUS"

echo ""
echo "=== Test 3: 不正トークンで 401 を返すこと ==="
AUTH_STATUS=$(curl -sk -o /dev/null -w "%{http_code}" \
    -X POST "https://localhost:${PROXY_PORT}/register" \
    -H "Authorization: Bearer wrong-token" \
    -H "Content-Type: application/json" \
    -d "{\"fqdn\": \"$BACKEND\"}")
assert_eq "不正トークン 401" "401" "$AUTH_STATUS"

echo ""
echo "=== Test 4: Host ヘッダがバックエンドの FQDN に書き換えられること ==="
RESPONSE=$(curl -sk "https://localhost:${PROXY_PORT}/")
echo "  レスポンス: $RESPONSE"
HOST_VALUE=$(echo "$RESPONSE" | grep -o '"host":"[^"]*"' | cut -d'"' -f4)
assert_eq "Host ヘッダ書き換え" "$BACKEND" "$HOST_VALUE"

echo ""
echo "=== Test 5: パスが正しく転送されること ==="
RESPONSE=$(curl -sk "https://localhost:${PROXY_PORT}/some/path")
PATH_VALUE=$(echo "$RESPONSE" | grep -o '"path":"[^"]*"' | cut -d'"' -f4)
assert_eq "パス転送" "/some/path" "$PATH_VALUE"

echo ""
if [ $PASS -eq 0 ]; then
    echo -e "${GREEN}すべてのテストが通過しました${NC}"
else
    echo -e "${RED}テストが失敗しました${NC}"
    exit 1
fi
