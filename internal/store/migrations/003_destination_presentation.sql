ALTER TABLE destinations
  ADD COLUMN formatting_enabled BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN signature_text TEXT NOT NULL DEFAULT '';

ALTER TABLE destinations
  ADD CONSTRAINT destinations_signature_text_check CHECK (char_length(signature_text) <= 200);
