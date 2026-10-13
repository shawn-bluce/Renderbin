-- idx_sessions_expires_at never served a query: the only statement that filters
-- on expires_at wraps it in unixepoch(), which keeps SQLite from using a plain
-- index (EXPLAIN QUERY PLAN shows a full scan), and session lookups go through
-- the primary key or idx_sessions_user_id. It only added work to every login.
DROP INDEX IF EXISTS idx_sessions_expires_at;
