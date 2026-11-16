CREATE TABLE xero_tenants (
    tenant_id varchar(64) NOT NULL,
    tenant_name varchar(255) NOT NULL,
    access_token_enc text NULL,
    refresh_token_enc text NULL,
    token_expiry datetime(6) NULL,
    scopes varchar(512) NULL,
    auth_status varchar(32) NOT NULL DEFAULT 'active',
    created_at datetime(6) NOT NULL,
    updated_at datetime(6) NOT NULL,
    PRIMARY KEY (tenant_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
