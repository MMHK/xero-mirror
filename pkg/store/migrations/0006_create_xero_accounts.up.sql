CREATE TABLE xero_accounts (
    tenant_id varchar(64) NOT NULL,
    account_id varchar(64) NOT NULL,
    code varchar(32) NULL,
    name varchar(255) NULL,
    type varchar(32) NULL,
    status varchar(32) NULL,
    xero_updated_at datetime(6) NULL,
    synced_at datetime(6) NOT NULL,
    PRIMARY KEY (tenant_id, account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
