-- A comment can be an AI explanation of the quoted passage, requested by the
-- reader named as its author and shown with an AI marker. Ordinary comments
-- are 0. Keep this file pure ASCII (see 0014_file_comments.sql).
ALTER TABLE file_comments ADD COLUMN is_ai BOOLEAN NOT NULL DEFAULT 0;
