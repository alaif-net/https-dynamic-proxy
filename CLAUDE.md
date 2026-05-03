# https-dynamic-proxy

## 概要

起動するたびにFQDNが変化するバックエンドHTTPSサーバーに対して、固定URLでアクセスするためのリバースプロキシサーバー。

## 技術スタック

- **言語:** Go 1.22
- **TLS:** `golang.org/x/crypto/acme/autocert`（Let's Encrypt自動取得・更新）
- **プロキシ:** `net/http/httputil.ReverseProxy`
- **Docker:** マルチステージビルド、`scratch` ベースイメージ（~6MB）

## ディレクトリ構成

```
main.go              # プロキシ本体
Dockerfile           # マルチステージビルド
docker-compose.yml   # 本番用Compose設定
.env.example         # 環境変数テンプレート
test/
  run.sh             # 結合テスト（Docker使用）
  backend/           # テスト用バックエンドサーバー
```

## 環境変数

| 変数名 | 必須 | デフォルト | 説明 |
|--------|:----:|-----------|------|
| `PROXY_DOMAIN` | ✅ | - | プロキシの固定FQDN |
| `REGISTER_TOKEN` | ✅ | - | 管理API認証トークン |
| `BACKEND_TLS_VERIFY` | | `true` | `false` で自己署名証明書を許可 |
| `DATA_DIR` | | `/data` | 状態・証明書キャッシュの保存先 |
| `DEV_TLS_CERT` | | - | 開発用TLS証明書パス（設定時はLet's Encryptを使用しない） |
| `DEV_TLS_KEY` | | - | 開発用TLS秘密鍵パス |

## 管理API

全エンドポイントに `Authorization: Bearer <REGISTER_TOKEN>` が必要。

| メソッド | パス | 説明 |
|---------|------|------|
| `GET` | `/backends` | 登録一覧 |
| `POST` | `/backends` | 登録（`name` 省略でデフォルト） |
| `DELETE` | `/backends` | デフォルト削除 |
| `DELETE` | `/backends/{name}` | 名前付き削除 |

## ルーティング

```
/{name}/rest → 名前付きバックエンドへ（プレフィックス除去して転送）
/rest        → デフォルトバックエンドへ（パスそのまま転送）
```

名前付きが優先。どちらも未登録なら 503。

## 開発・テスト

```bash
# 結合テスト（自己署名証明書＋Dockerネットワークで完結）
bash test/run.sh
```

テストは以下を検証する：
1. バックエンド未登録時に 503 を返すこと
2. 不正トークンで 401 を返すこと
3. デフォルトバックエンドを登録できること
4. `Host` ヘッダがデフォルトバックエンドのFQDNに書き換えられること
5. 名前付きバックエンドを登録できること
6. 名前付きバックエンドへのルーティングと `Host` ヘッダ書き換え
7. パスプレフィックスが除去されて転送されること
8. 未一致パスはデフォルトバックエンドへフォールバックすること
9. バックエンド一覧を取得できること
10. 名前付きバックエンドを削除できること
11. 予約語（`backends`）は登録不可なこと

