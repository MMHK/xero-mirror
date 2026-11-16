CREATE TABLE xero_payments (
    tenant_id varchar(64) NOT NULL,
    payment_id varchar(64) NOT NULL,
    invoice_number varchar(64) NULL,
    contact_id varchar(64) NULL,
    amount decimal(18,4) NOT NULL DEFAULT 0,
    date datetime(6) NULL,
    status varchar(32) NULL,
    xero_updated_at datetime(6) NULL,
    synced_at datetime(6) NOT NULL,
    PRIMARY KEY (tenant_id, payment_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
