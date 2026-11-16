CREATE TABLE sync_state (
    tenant_id varchar(64) NOT NULL,
    entity varchar(32) NOT NULL,
    cursor_time datetime(6) NULL,
    last_run_at datetime(6) NULL,
    status varchar(32) NOT NULL DEFAULT 'idle',
    last_error text NULL,
    PRIMARY KEY (tenant_id, entity)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
