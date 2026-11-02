CREATE TABLE IF NOT EXISTS x_oauth_requests (
  request_token TEXT PRIMARY KEY,
  destination_id BIGINT NOT NULL REFERENCES destinations(id) ON DELETE CASCADE,
  secret_cipher BYTEA NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS x_connections (
  destination_id BIGINT PRIMARY KEY REFERENCES destinations(id) ON DELETE CASCADE,
  username TEXT NOT NULL,
  token_cipher BYTEA NOT NULL,
  secret_cipher BYTEA NOT NULL,
  connected_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS x_oauth_requests_expires_idx ON x_oauth_requests(expires_at);
