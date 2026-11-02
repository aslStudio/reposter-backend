ALTER TABLE destinations DROP CONSTRAINT IF EXISTS destinations_daily_post_limit_check;
ALTER TABLE destinations ADD CONSTRAINT destinations_daily_post_limit_check CHECK (daily_post_limit >= 0);
