CREATE TABLE webhook_events (
    event_id varchar(64) NOT NULL,
    tenant_id varchar(64) NOT NULL,
    entity varchar(32) NOT NULL,
    xero_id varchar(64) NOT NULL,
    event_date datetime(6) NULL,
    received_at datetime(6) NOT NULL,
    processed_at datetime(6) NULL,
    PRIMARY KEY (event_id),
    INDEX idx_webhook_events_resource (tenant_id, entity, xero_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
