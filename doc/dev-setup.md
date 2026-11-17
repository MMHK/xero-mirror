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
```

**產生 TOKEN_ENC_KEY**：

```bash
openssl rand -base64 32
```

### 3. 建置與執行

```bash
# 建置
go build -o xero-mirror.exe .

# 首次授權（會開啟瀏覽器）
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

## 測試環境

測試用獨立的 DB 與 port，避免污染 dev 資料：

```bash
# 啟動測試 MySQL（port 13315）
docker run -d --name xero-mirror-test-mysql \
  -e MYSQL_ROOT_PASSWORD=root \
  -e MYSQL_DATABASE=xero_mirror_test \
  -p 13315:3306 \
  mysql:8.4

# 跑測試
TEST_MYSQL_DSN="root:root@tcp(127.0.0.1:13315)/xero_mirror_test?parseTime=true&loc=UTC" \
  go test ./... -count=1

# 清除
docker rm -f xero-mirror-test-mysql
```

任務 11 會建立 `docker-compose.test.yml` 統一管理。

## 相關文件

- `AGENTS.md` §10：指令與測試規則
- `doc/spec.md` §8：環境變數
- `pkg/store/migrations/`：schema 定義
