# HTTPS動的プロキシサーバー 仕様書

## 概要

起動する度にFQDNが変化するバックエンドHTTPSサーバーに対して、固定URLでアクセスするためのリバースプロキシサーバー。

## ネットワーク構成

```
クライアント
    │ HTTPS (固定FQDN)
    ▼
[プロキシ :443]  ←→  :80 (Let's Encrypt HTTP-01 チャレンジ + HTTPリダイレクト)
    │ ルーティング + Host書き換えて転送
    ▼
[バックエンド HTTPS] (起動時にFQDNを登録)
```

## 実装

- **言語：** Go 1.22
- **Dockerイメージ：** マルチステージビルド → `scratch` ベース（~6MB）

## TLS証明書

- Let's Encrypt ACME **HTTP-01チャレンジ**で自動取得・自動更新
- `golang.org/x/crypto/acme/autocert` パッケージを使用
- 有効期限30日前にバックグラウンドで自動更新
- ポート80は `.well-known/acme-challenge/` のみ応答、それ以外はHTTPS（443）へリダイレクト
- 証明書は `DATA_DIR` にキャッシュ（再起動後も再利用）

## ルーティング

### 優先順位

```
リクエスト: GET /{seg}/rest...

1. {seg} が名前付きバックエンドに一致
   → プレフィックス /{seg} を取り除いてバックエンドへ転送

2. デフォルトバックエンドが登録済み
   → パスをそのままバックエンドへ転送

3. どちらも未登録
   → 503 Service Unavailable
```

### HOSTヘッダ書き換え

バックエンドはHOSTベースのルーティングを行うため、プロキシは転送時にHOSTヘッダをバックエンドのFQDNに上書きする。

```
クライアント送信:  Host: proxy.example.com
プロキシが転送:   Host: dynamic-xxx.example.com  ← 登録済みFQDNに上書き
```

## エンドポイント

| メソッド | ポート | パス | 説明 |
|---------|--------|------|------|
| `GET` | 443 | `/backends` | バックエンド一覧 |
| `POST` | 443 | `/backends` | バックエンド登録 |
| `DELETE` | 443 | `/backends` | デフォルトバックエンド削除 |
| `DELETE` | 443 | `/backends/{name}` | 名前付きバックエンド削除 |
| `GET` | 80 | `/.well-known/acme-challenge/*` | Let's Encrypt チャレンジ |
| `*` | 80 | それ以外 | HTTPS（443）へリダイレクト |
| `*` | 443 | それ以外 | バックエンドへ透過プロキシ |

全管理APIエンドポイントは `Authorization: Bearer <REGISTER_TOKEN>` が必要。

## 管理API

### `GET /backends`

登録済みバックエンドの一覧を返す。

**レスポンス**

```json
{
  "default": { "fqdn": "xxx.example.com", "port": "8443" },
  "named": {
    "qwen3-27b": { "fqdn": "yyy.example.com" }
  }
}
```

### `POST /backends`

バックエンドを登録する。`name` を省略するとデフォルトバックエンドとして登録される。

**リクエスト**

```json
{ "fqdn": "xxx.example.com" }                         // デフォルト登録
{ "fqdn": "xxx.example.com", "port": "8443" }          // ポート指定
{ "name": "qwen3-27b", "fqdn": "xxx.example.com" }     // 名前付き登録
```

| フィールド | 必須 | 説明 |
|-----------|:----:|------|
| `name` | | ルーティングキー。省略時はデフォルト |
| `fqdn` | ✅ | バックエンドのFQDN |
| `port` | | ポート番号。省略時は443 |

**名前のバリデーション**

- 使用可能文字: `[a-zA-Z0-9_-]+`
- 予約済み: `backends`

**レスポンス**

| ステータス | 説明 |
|-----------|------|
| `200 OK` | 登録成功 |
| `400 Bad Request` | リクエストボディ不正・無効な名前・予約語 |
| `401 Unauthorized` | トークン不正 |

### `DELETE /backends`

デフォルトバックエンドを削除する。

### `DELETE /backends/{name}`

指定した名前付きバックエンドを削除する。

## 環境変数

| 変数名 | デフォルト | 必須 | 説明 |
|--------|-----------|:----:|------|
| `PROXY_DOMAIN` | - | ✅ | プロキシ自身の固定FQDN |
| `REGISTER_TOKEN` | - | ✅ | 管理API認証トークン |
| `BACKEND_TLS_VERIFY` | `true` | | `false` で自己署名証明書を許可 |
| `DATA_DIR` | `/data` | | 状態・証明書キャッシュの保存先 |
| `DEV_TLS_CERT` | - | | 開発用TLS証明書パス（設定時はLet's Encryptを使用しない） |
| `DEV_TLS_KEY` | - | | 開発用TLS秘密鍵パス |

## Dockerボリューム

```
/data/
├── backends.json   # 登録済みバックエンド（デフォルト + 名前付き）
└── certs/          # Let's Encrypt 証明書キャッシュ
```

旧形式の `backend_fqdn.txt` が存在する場合、起動時にデフォルトバックエンドとして自動マイグレーションされる。
