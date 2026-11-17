-- 同 invoice number：Xero 上限 255 字元，放寬以免同類資料同步失敗。
ALTER TABLE xero_credit_notes MODIFY COLUMN number varchar(255) NULL;
