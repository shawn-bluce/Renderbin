-- The /res pages' "open on your phone" QR button is on by default. Boolean
-- configs read as false when missing, so the default has to be a row; OR IGNORE
-- leaves an existing choice alone.
INSERT OR IGNORE INTO configs (key, value) VALUES ('qr_widget', 'true');
