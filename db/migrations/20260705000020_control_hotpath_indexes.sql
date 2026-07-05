-- DueWatches (control.go) runs every daemonInterval (60s) per source, filtering
-- control.watch by (source, enabled) and ordering/filtering control.poll_state by
-- (next_due_at, backoff_until). Neither had an index beyond the primary keys, so
-- the hot path seq-scanned + sorted both tables on every tick.
CREATE INDEX watch_source_enabled_idx ON control.watch (source, enabled);
CREATE INDEX poll_state_next_due_at_idx ON control.poll_state (next_due_at);
CREATE INDEX poll_state_backoff_until_idx ON control.poll_state (backoff_until);
