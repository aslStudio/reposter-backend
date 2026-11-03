ALTER TABLE sources ADD COLUMN enabled BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE operator_settings (
  id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
  automation_enabled BOOLEAN NOT NULL DEFAULT false
);

INSERT INTO operator_settings(id, automation_enabled) VALUES(true, false);
