ALTER TABLE source_posts ADD COLUMN media_source_urls TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE drafts ADD COLUMN media_source_urls TEXT[] NOT NULL DEFAULT '{}';
