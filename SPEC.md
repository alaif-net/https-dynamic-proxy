# HTTPS動的プロキシサーバー 仕様書

## 概要

起動する度にFQDNが変化するバックエンドHTTPSサーバーに対して、固定URLでアクセスするためのリバースプロキシサーバー。

## ネットワーク構成

```
クライアント
    │ HTTPS (固定FQDN)
    ▼
[プロキシ :443]  ←→  :80 (Let's Encrypt HTTP-01 チャレンジ + HTTPリダイレクト)
    │ Host: <バックエンドFQDN> に書き換えて転送
    ▼
[バックエンド HTTPS] (起動時にFQDNを登録)
```

## 実装

- **言語：** Go
- **Dockerイメージ：** マルチステージビルド → `scratch` ベース（~15MB）

## TLS証明書

- Let's Encrypt ACME **HTTP-01チャレンジ**で自動取得・自動更新
- `golang.org/x/crypto/acme/autocert` パッケージを使用
- 有効期限30日前にバックグラウンドで自動更新
- ポート80は `.well-known/acme-challenge/` のみ応答、それ以外はHTTPS（443）へリダイレクト
- 証明書は `DATA_DIR` にキャッシュ（再起動後も再利用）

## エンドポイント

| メソッド | ポート | パス | 説明 |
|---------|--------|------|------|
| `POST` | 443 | `/register` | バックエンドFQDN登録 |
| `GET` | 80 | `/.well-known/acme-challenge/*` | Let's Encrypt チャレンジ |
| `GET` | 80 | それ以外 | HTTPS（443）へリダイレクト |
| 全て | 443 | それ以外 | バックエンドへ透過プロキシ |

## 登録API

### `POST /register`

**リクエスト**

```
Authorization: Bearer <TOKEN>
Content-Type: application/json

{"fqdn": "xxx.example.com"}
```

**レスポンス**

| ステータス | 説明 |
|-----------|------|
| `200 OK` | 登録成功 |
| `401 Unauthorized` | トークン不正 |
| `400 Bad Request` | ボディ不正 |

- 登録されたFQDNはファイルに永続化（プロキシ再起動後も保持）

## HOSTヘッダ書き換え

バックエンドサーバーはHOSTベースのルーティングを行うため、プロキシはリクエスト転送時にHOSTヘッダを上書きする。

```
クライアント送信:   Host: proxy.example.com
プロキシが転送:    Host: dynamic-xxx.example.com  ← 登録済みFQDNに上書き
```

## プロキシ動作

- 登録された最新FQDNへリクエストを転送
- バックエンド未登録時は `503 Service Unavailable`
- バックエンドのTLS証明書検証：`BACKEND_TLS_VERIFY` で制御（自己署名証明書に対応）

## 環境変数

| 変数名 | デフォルト | 必須 | 説明 |
|--------|-----------|------|------|
| `PROXY_DOMAIN` | - | ✅ | プロキシ自身の固定FQDN |
| `REGISTER_TOKEN` | - | ✅ | 登録API認証トークン |
| `BACKEND_TLS_VERIFY` | `true` | - | `false` で自己署名証明書を許可 |
| `DATA_DIR` | `/data` | - | FQDN・証明書キャッシュの保存先 |

## Dockerボリューム

```
/data/  ← 証明書キャッシュ + 登録FQDN の永続化（ここだけマウントすればOK）
```
