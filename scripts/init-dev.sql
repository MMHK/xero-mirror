-- xero-mirror dev database initialization
-- 由 docker-entrypoint-initdb.d 自動執行（只在首次建立 volume 時）

-- 確保 DB 使用正確的 character set（MYSQL_DATABASE 環境變數已建立 DB，但需確認 collation）
ALTER DATABASE xero_mirror CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- 建立 dev 專用用戶（選用，方便本機工具連線）
-- 密碼 'root' 只用於本機 dev，不要用於正式環境
CREATE USER IF NOT EXISTS 'xero_dev'@'%' IDENTIFIED BY 'xero_dev_pass';
GRANT ALL PRIVILEGES ON xero_mirror.* TO 'xero_dev'@'%';
FLUSH PRIVILEGES;

-- 驗證（可刪除，只供首次啟動確認）
SELECT '✅ xero_mirror database initialized with utf8mb4_unicode_ci' AS status;
