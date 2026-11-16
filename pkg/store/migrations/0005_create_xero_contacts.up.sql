CREATE TABLE xero_contacts (
    tenant_id varchar(64) NOT NULL,
    contact_id varchar(64) NOT NULL,
    name varchar(255) NULL,
    status varchar(32) NULL,
    xero_updated_at datetime(6) NULL,
    synced_at datetime(6) NOT NULL,
    PRIMARY KEY (tenant_id, contact_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
