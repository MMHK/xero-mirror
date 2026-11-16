CREATE TABLE sync_jobs (
    id bigint unsigned NOT NULL AUTO_INCREMENT,
    tenant_id varchar(64) NOT NULL,
    entity varchar(32) NOT NULL,
    xero_id varchar(64) NOT NULL,
    reason varchar(64) NOT NULL,
    status varchar(32) NOT NULL DEFAULT 'pending',
    created_at datetime(6) NOT NULL,
    finished_at datetime(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uniq_sync_jobs_resource (tenant_id, entity, xero_id),
    INDEX idx_sync_jobs_status (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
