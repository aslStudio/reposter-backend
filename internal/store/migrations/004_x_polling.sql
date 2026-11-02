ALTER TABLE sources ADD COLUMN external_user_id TEXT NOT NULL DEFAULT '',
  ADD COLUMN last_polled_at TIMESTAMPTZ;
