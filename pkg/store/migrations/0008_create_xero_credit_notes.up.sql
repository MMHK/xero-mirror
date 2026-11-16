CREATE TABLE xero_credit_notes (
    tenant_id varchar(64) NOT NULL,
    credit_note_id varchar(64) NOT NULL,
    type varchar(32) NULL,
    number varchar(64) NULL,
    contact_id varchar(64) NULL,
    status varchar(32) NULL,
    date datetime(6) NULL,
    due_date datetime(6) NULL,
    total decimal(18,4) NOT NULL DEFAULT 0,
    amount_due decimal(18,4) NOT NULL DEFAULT 0,
    remaining_credit decimal(18,4) NOT NULL DEFAULT 0,
    currency varchar(8) NULL,
    xero_updated_at datetime(6) NULL,
    synced_at datetime(6) NOT NULL,
    PRIMARY KEY (tenant_id, credit_note_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
