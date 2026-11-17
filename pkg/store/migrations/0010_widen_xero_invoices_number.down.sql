-- 回滾：改回 varchar(64)；若已有超過 64 字元的資料會失敗。
ALTER TABLE xero_invoices MODIFY COLUMN number varchar(64) NULL;
