CREATE TABLE IF NOT EXISTS sources (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  platform TEXT NOT NULL CHECK (platform IN ('telegram', 'x', 'threads')),
  handle TEXT NOT NULL,
  url TEXT NOT NULL,
  cursor TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (platform, handle)
);

CREATE TABLE IF NOT EXISTS destinations (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  platform TEXT NOT NULL CHECK (platform IN ('telegram', 'x', 'threads')),
  external_id TEXT NOT NULL,
  style_prompt TEXT NOT NULL,
  mode TEXT NOT NULL CHECK (mode IN ('manual_approval', 'automatic')),
  daily_post_limit INTEGER NOT NULL CHECK (daily_post_limit > 0),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (platform, external_id)
);

CREATE TABLE IF NOT EXISTS routes (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  source_id BIGINT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  destination_id BIGINT NOT NULL REFERENCES destinations(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (source_id, destination_id)
);

CREATE TABLE IF NOT EXISTS source_posts (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  source_id BIGINT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  external_id TEXT NOT NULL,
  original_url TEXT NOT NULL,
  body TEXT NOT NULL,
  media_paths TEXT[] NOT NULL DEFAULT '{}',
  published_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (source_id, external_id)
);

CREATE TABLE IF NOT EXISTS drafts (
  id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  route_id BIGINT NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
  source_post_id BIGINT NOT NULL REFERENCES source_posts(id) ON DELETE CASCADE,
  destination_id BIGINT NOT NULL REFERENCES destinations(id) ON DELETE CASCADE,
  body TEXT NOT NULL,
  media_paths TEXT[] NOT NULL DEFAULT '{}',
  status TEXT NOT NULL CHECK (status IN ('pending_approval', 'ready_to_publish', 'publishing', 'published', 'publish_failed')),
  external_post_id TEXT,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  published_at TIMESTAMPTZ,
  UNIQUE (route_id, source_post_id)
);

CREATE INDEX IF NOT EXISTS drafts_status_created_idx ON drafts(status, created_at);
CREATE INDEX IF NOT EXISTS drafts_destination_published_idx ON drafts(destination_id, published_at) WHERE status = 'published';
