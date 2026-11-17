-- Xero InvoiceNumber 上限 255 字元，原 varchar(64) 會造成 Data too long。
ALTER TABLE xero_invoices MODIFY COLUMN number varchar(255) NULL;
