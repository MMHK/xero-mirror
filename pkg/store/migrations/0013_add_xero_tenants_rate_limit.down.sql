ALTER TABLE xero_tenants
  DROP COLUMN day_limit_remaining,
  DROP COLUMN minute_limit_remaining,
  DROP COLUMN app_minute_limit_remaining,
  DROP COLUMN rate_limit_snapshot_at;
