# https-dynamic-proxy

起動するたびにFQDNが変化するバックエンドHTTPSサーバーに対して、固定URLでアクセスするためのリバースプロキシサーバー。

## 仕組み

```
クライアント
    │ HTTPS (固定FQDN)
    ▼
[プロキシ :443]  ←→  :80 (Let's Encrypt HTTP-01 チャレンジ + HTTPリダイレクト)
    │ Host: <バックエンドFQDN> に書き換えて転送
    ▼
[バックエンド HTTPS] (起動時にFQDNを登録)
```

バックエンドサーバーは起動時に自身のFQDNをプロキシへ登録する。以降のリクエストはすべて登録済みFQDNへ転送され、`Host` ヘッダもバックエンドのFQDNに書き換えられる。

## セットアップ

### 前提条件

- Docker / Docker Compose
- プロキシサーバーのポート80・443が外部からアクセス可能なこと（Let's Encrypt証明書取得に必要）
- `PROXY_DOMAIN` に設定するドメインのDNSがプロキシサーバーのIPを向いていること

### 起動

```bash
cp .env.example .env
```

`.env` を編集する。

```env
PROXY_DOMAIN=proxy.example.com      # プロキシの固定ドメイン
REGISTER_TOKEN=your-secret-token    # バックエンド登録用トークン
BACKEND_TLS_VERIFY=true             # 自己署名証明書のバックエンドには false を指定
```

```bash
docker compose up -d
```

初回アクセス時にLet's Encryptから証明書が自動取得される。証明書は `/data/certs/` にキャッシュされ、有効期限30日前に自動更新される。

## バックエンドの登録

バックエンドサーバーの起動スクリプトに以下を追加する。

```bash
curl -X POST https://proxy.example.com/register \
  -H "Authorization: Bearer your-secret-token" \
  -H "Content-Type: application/json" \
  -d '{"fqdn": "dynamic-xxx.example.com"}'
```

登録されたFQDNは `/data/backend_fqdn.txt` に永続化されるため、プロキシを再起動しても保持される。

## 環境変数

| 変数名 | デフォルト | 必須 | 説明 |
|--------|-----------|:----:|------|
| `PROXY_DOMAIN` | - | ✅ | プロキシ自身の固定FQDN |
| `REGISTER_TOKEN` | - | ✅ | 登録API認証トークン |
| `BACKEND_TLS_VERIFY` | `true` | | `false` で自己署名証明書を許可 |
| `DATA_DIR` | `/data` | | FQDN・証明書キャッシュの保存先 |

## APIリファレンス

### `POST /register`

バックエンドのFQDNを登録する。

**リクエスト**

```
Authorization: Bearer <REGISTER_TOKEN>
Content-Type: application/json

{"fqdn": "dynamic-xxx.example.com"}
```

**レスポンス**

| ステータス | 説明 |
|-----------|------|
| `200 OK` | 登録成功 |
| `400 Bad Request` | リクエストボディ不正 |
| `401 Unauthorized` | トークン不正 |
| `405 Method Not Allowed` | POST以外のメソッド |

バックエンド未登録の状態でプロキシへアクセスすると `503 Service Unavailable` が返る。

## データ永続化

コンテナの `/data` ディレクトリをホストにマウントする（`docker-compose.yml` でデフォルト設定済み）。

```
/data/
├── certs/              # Let's Encrypt 証明書キャッシュ
└── backend_fqdn.txt    # 登録済みバックエンドFQDN
```

## ビルド

```bash
docker build -t https-dynamic-proxy .
```

Go 1.22 + マルチステージビルドにより `scratch` ベースの最小イメージ（約6MB）を生成する。
