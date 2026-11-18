CREATE TABLE failed_sync_jobs (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    tenant_id varchar(64) NOT NULL,
    entity varchar(32) NOT NULL,
    xero_id varchar(64) NOT NULL,
    reason varchar(64) NOT NULL,
    last_error text,
    failed_at datetime(6) NOT NULL,
    PRIMARY KEY (id),
    INDEX idx_failed_sync_jobs_tenant_entity (tenant_id, entity),
    INDEX idx_failed_sync_jobs_failed_at (failed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
