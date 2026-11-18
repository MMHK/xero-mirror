ALTER TABLE xero_tenants
  ADD COLUMN day_limit_remaining INT NULL,
  ADD COLUMN minute_limit_remaining INT NULL,
  ADD COLUMN app_minute_limit_remaining INT NULL,
  ADD COLUMN rate_limit_snapshot_at DATETIME(6) NULL;
