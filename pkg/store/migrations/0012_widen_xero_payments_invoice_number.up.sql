-- Payment 的 invoice_number 取自同一種 Xero 發票號，一併放寬到 255 字元。
ALTER TABLE xero_payments MODIFY COLUMN invoice_number varchar(255) NULL;
