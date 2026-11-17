-- 回滾：改回 varchar(64)；若已有超過 64 字元的資料會失敗。
ALTER TABLE xero_payments MODIFY COLUMN invoice_number varchar(64) NULL;
