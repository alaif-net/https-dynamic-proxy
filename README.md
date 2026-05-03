# https-dynamic-proxy

起動するたびにFQDNが変化するバックエンドHTTPSサーバーに対して、固定URLでアクセスするためのリバースプロキシサーバー。

詳細な仕様は [SPEC.md](SPEC.md) を参照。

## セットアップ

### 前提条件

- Docker / Docker Compose
- プロキシサーバーのポート80・443が外部からアクセス可能なこと（Let's Encrypt証明書取得に必要）
- `PROXY_DOMAIN` に設定するドメインのDNSがプロキシサーバーのIPを向いていること

### 起動

```bash
cp .env.example .env
# .env を編集して PROXY_DOMAIN と REGISTER_TOKEN を設定
docker compose up -d
```

初回アクセス時にLet's Encryptから証明書が自動取得され、有効期限30日前に自動更新される。

## バックエンドの登録

```bash
# デフォルトバックエンド
curl -X POST https://proxy.example.com/backends \
  -H "Authorization: Bearer your-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"fqdn": "dynamic-xxx.example.com"}'

# 名前付きバックエンド（パスプレフィックスでルーティング）
curl -X POST https://proxy.example.com/backends \
  -H "Authorization: Bearer your-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"name": "myservice", "fqdn": "dynamic-xxx.example.com"}'
```

## ビルド

```bash
docker build -t https-dynamic-proxy .
```

Go 1.22 + マルチステージビルドにより `scratch` ベースの最小イメージ（約6MB）を生成する。
