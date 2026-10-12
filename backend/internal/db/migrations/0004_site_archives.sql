-- Directory sites: a file of kind 'site' is a whole directory (index.html plus
-- the pages, stylesheets and scripts it links to), stored as one zip in
-- files.archive. html_content is '' for those rows, and archive is NULL for
-- every other kind. See internal/site for the format and its limits.
--
-- Keep this file pure ASCII -- sqlc reads this directory for its schema, and
-- its SQLite rewriter works on byte offsets while the parser reports character
-- positions, so one multi-byte character silently corrupts codegen.
--
-- A column rather than a table of per-file rows on purpose: backup.Restore
-- copies tables one by one in name order, and a child table with a cascading
-- foreign key to files would be emptied again by the DELETE on files that runs
-- after it. A column travels with the row it belongs to.
ALTER TABLE files ADD COLUMN archive BLOB;

-- content_size now counts both places content can live. The triggers from
-- 0003 only knew html_content: left alone, they would reset a site's size to
-- 0 whenever something inserted the row with a size they disagree with --
-- which is exactly what a backup restore does -- and the site would stop
-- counting against its owner's quota.
DROP TRIGGER files_content_size_after_insert;
DROP TRIGGER files_content_size_after_update;

CREATE TRIGGER files_content_size_after_insert
AFTER INSERT ON files
WHEN NEW.content_size <> length(CAST(NEW.html_content AS BLOB)) + COALESCE(length(NEW.archive), 0)
BEGIN
    UPDATE files SET content_size = length(CAST(NEW.html_content AS BLOB)) + COALESCE(length(NEW.archive), 0)
    WHERE id = NEW.id;
END;

-- Still AFTER UPDATE OF specific columns, so the counter bumps every /res
-- request issues never fire it.
CREATE TRIGGER files_content_size_after_update
AFTER UPDATE OF html_content, archive ON files
WHEN NEW.content_size <> length(CAST(NEW.html_content AS BLOB)) + COALESCE(length(NEW.archive), 0)
BEGIN
    UPDATE files SET content_size = length(CAST(NEW.html_content AS BLOB)) + COALESCE(length(NEW.archive), 0)
    WHERE id = NEW.id;
END;
