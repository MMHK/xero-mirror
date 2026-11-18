# 本機開發環境設定

> **⚠️ AI 生成聲明**：本文件由 AI 輔助生成，僅供參考。

## 前置需求

- Docker Desktop（或相容的 Docker 環境）
- Go 1.24+
- MySQL 8.4（由 Docker 提供，不需要本機安裝）

## 快速啟動

### 1. 啟動 MySQL

```bash
docker compose up -d
```

首次啟動會：
- 下載 `mysql:8.4` 映像檔（約 600MB）
- 建立 Docker volume `xero-mirror_mysql-data`
- 執行 `scripts/init-dev.sql` 初始化 DB
- 啟動 healthcheck（約 30 秒後就緒）

檢查狀態：

```bash
docker compose ps
# 應該看到 STATUS: healthy
```

### 2. 設定環境變數

複製 `.env.example` 為 `.env`：

```bash
cp .env.example .env
```

編輯 `.env`，填入 Xero OAuth2 憑證：

```env
XERO_CLIENT_ID=your_client_id
XERO_CLIENT_SECRET=your_client_secret
XERO_REDIRECT_URI=http://localhost:8080/callback
XERO_WEBHOOK_KEY=your_webhook_key
XERO_TOKEN_ENC_KEY=your_32_bytes_base64_key  # openssl rand -base64 32
MYSQL_DSN=root:root@tcp(127.0.0.1:3307)/xero_mirror?parseTime=true&loc=UTC
SYNC_CRON=*/5 * * * *
HTTP_ADDR=:8080
LOG_LEVEL=  # 選用：debug|info|warn|error，留空預設 info；要排查分塊寫入時再改成 debug
```

**產生 TOKEN_ENC_KEY**：

```bash
openssl rand -base64 32
```

### 3. 建置與執行

```bash
# 建置
go build -o xero-mirror.exe .

# 首次授權（會印出授權 URL，請手動在瀏覽器開啟）
./xero-mirror.exe auth

# 啟動常駐服務（cron + webhook + admin）
./xero-mirror.exe serve
```

## 連線資訊

| 項目 | 值 |
|------|-----|
| Host | `127.0.0.1` |
| Port | `3307`（host）→ `3306`（container） |
| User | `root` |
| Password | `root` |
| Database | `xero_mirror` |
| DSN | `root:root@tcp(127.0.0.1:3307)/xero_mirror?parseTime=true&loc=UTC` |

**本機 MySQL 工具連線**：

```bash
# 命令列
mysql -h 127.0.0.1 -P 3307 -u root -proot xero_mirror

# 或用 xero_dev 用戶（密碼 xero_dev_pass）
mysql -h 127.0.0.1 -P 3307 -u xero_dev -pxero_dev_pass xero_mirror
```

## 常用指令

```bash
# 啟動
docker compose up -d

# 停止（保留資料）
docker compose down

# 停止並清除資料（⚠️ 會刪除所有 DB 資料）
docker compose down -v

# 查看 log
docker compose logs -f mysql

# 進入 MySQL shell
docker compose exec mysql mysql -u root -proot xero_mirror

# 重新啟動（套用設定變更）
docker compose restart
```

## 資料持久化

Docker volume `xero-mirror_mysql-data` 會持久化 MySQL 資料。

- `docker compose down` → 保留 volume（資料不丟）
- `docker compose down -v` → 刪除 volume（⚠️ 資料全丟）
- 首次 `up` 會自動執行 `scripts/init-dev.sql`

## 疑難排解

### Port 3307 已被佔用

編輯 `docker-compose.yml`，改 `ports` 映射：

```yaml
ports:
  - "13306:3306"  # 改成 13306 或其他
```

並更新 `.env` 的 `MYSQL_DSN`：

```env
MYSQL_DSN=root:root@tcp(127.0.0.1:13306)/xero_mirror?parseTime=true&loc=UTC
```

### MySQL 啟動失敗

檢查 log：

```bash
docker compose logs mysql
```

常見問題：
- Port 衝突 → 改 `ports` 映射
- Volume 損壞 → `docker compose down -v` 重建（⚠️ 資料全丟）

### Migration 未自動套用

`xero-mirror` 啟動時會自動套用 migration（`pkg/store/migrate.go`）。

如果 migration 未套用：
1. 確認 `MYSQL_DSN` 正確
2. 檢查 `xero-mirror` 啟動 log（應有 `migration applied=N`）
3. 手動檢查 `schema_migrations` 表：

```bash
docker compose exec mysql mysql -u root -proot xero_mirror -e "SELECT * FROM schema_migrations;"
```

## Live Xero 測試

`pkg/xero/live_test.go` 是 opt-in 的真實 API 整合測試，只做 GET 唯讀呼叫。未設定環境變數時相關測試一律 skip，預設 `go test ./...` 不會打真實 Xero、不消耗配額。

### 取得 tenantId

- 以有效 access token 呼叫 `GET https://api.xero.com/connections`，回應中的 `tenantId` 即為所需值。
- 或查詢本機 DB 已授權的 `xero_tenants` 表。

### 取得 access token

- 透過本服務 `go run . auth` 授權後，從 DB 的加密 token 解密取得；或用 Xero 開發者工具手動取得。
- access token 效期約 **30 分鐘**，過期後測試會失敗，需手動重新取得；測試不會自動 refresh。
- 僅透過環境變數傳入，禁止寫進 `.env.example` 或 commit；`.env` 已被 gitignore 忽略。

### 執行

```bash
TEST_XERO_TENANT_ID=... TEST_XERO_ACCESS_TOKEN=... \
  go test ./pkg/xero -run TestLive -v
```

- 每次執行僅 3 次 GET（`/connections`、`/Invoices`、`/Contacts` 各一次），不寫入任何資料。
- 不設 `TEST_XERO_*` 時，三個 `TestLive*` 案例全部 SKIP，`go test ./...` 維持全綠且零配額。

## 測試環境

測試用獨立的 DB 與 port，避免污染 dev 資料（`docker-compose.test.yml`，拋棄式、無 volume）：

```bash
# 啟動測試 MySQL（port 13315）
docker compose -f docker-compose.test.yml up -d

# 跑測試（需要 DB 的整合測試）
TEST_MYSQL_DSN="root:root@tcp(127.0.0.1:13315)/xero_mirror_test?parseTime=true&loc=UTC" \
  go test ./... -count=1

# 停止（測試庫可拋棄，不需保留資料）
docker compose -f docker-compose.test.yml down
```

跨 package 的測試 helper 集中在 `tests/testing.go`：`RequireTestDatabase`（檢查 DSN 指向 `_test` 庫）、`LockTestDatabase`（取 `xero-mirror:test` advisory lock，避免各 package 的 DROP TABLE／migration 互踩）、`OpenTestDB`。未設定 `TEST_MYSQL_DSN` 或連不上 DB 時相關測試一律 skip，`go test ./...` 不會直接紅。

## 相關文件

- `AGENTS.md` §10：指令與測試規則
- `doc/spec.md` §8：環境變數
- `pkg/store/migrations/`：schema 定義

## CI/CD（GoReleaser）

Push semver tag（`v*`）時，GitHub Actions 會自動建構 multi-arch Docker image 並推送 Docker Hub。

### 本機測試 GoReleaser

需先安裝 [GoReleaser](https://goreleaser.com/install/)：

```bash
# 檢查設定是否正確
goreleaser check

# 模擬建構（不推送，產出在 dist/）
goreleaser release --snapshot --clean

# 只建構單一平台（較快）
goreleaser build --single-target --snapshot --clean
```

建構產物在 `dist/` 目錄（已 gitignore）：
- `dist/xero-mirror_linux_amd64/xero-mirror`
- `dist/xero-mirror_linux_arm64/xero-mirror`
- `dist/checksums.txt`
- `dist/*.tar.gz`

### 觸發正式建構

```bash
git tag v1.0.0
git push origin v1.0.0
```

需要設定 GitHub Secrets：
- `DOCKERHUB_USERNAME`：Docker Hub 帳號
- `DOCKERHUB_TOKEN`：Docker Hub Access Token

詳細設計見 `doc/plans/2026-11-18-goreleaser-ci.md`。
