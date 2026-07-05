package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"s4rciv.org/api/internal/port"
)

// Failure backoff bounds (independent of the normal poll cadence): a failing
// stream is retried soon and backs off exponentially with each consecutive
// failure, capped so a persistently broken stream is still revisited hourly
// rather than falling all the way back to the 24h cadence.
const (
	failureBackoffBase = 1 * time.Minute
	failureBackoffMax  = 1 * time.Hour
)

type ControlStore struct {
	pool *pgxpool.Pool
}

func NewControlStore(pool *pgxpool.Pool) *ControlStore { return &ControlStore{pool: pool} }

func (c *ControlStore) Source(ctx context.Context, source string) (port.SourceConfig, error) {
	var cfg port.SourceConfig
	var rateMs int
	err := c.pool.QueryRow(ctx, `
		SELECT source, base_url, rate_limit_ms, user_agent, enabled
		FROM control.source WHERE source = $1`, source,
	).Scan(&cfg.Source, &cfg.BaseURL, &rateMs, &cfg.UserAgent, &cfg.Enabled)
	if err != nil {
		return cfg, err
	}
	cfg.RateLimit = time.Duration(rateMs) * time.Millisecond
	return cfg, nil
}

func (c *ControlStore) DueWatches(ctx context.Context, source string, now time.Time, limit int) ([]port.Watch, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT w.stream_id, w.source, w.source_local_key, w.canonical_url
		FROM control.watch w
		LEFT JOIN control.poll_state p ON p.stream_id = w.stream_id
		WHERE w.source = $1 AND w.enabled
		  AND (p.next_due_at IS NULL OR p.next_due_at <= $2)
		  AND (p.backoff_until IS NULL OR p.backoff_until <= $2)
		ORDER BY p.next_due_at ASC NULLS FIRST
		LIMIT $3`, source, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []port.Watch
	for rows.Next() {
		var w port.Watch
		if err := rows.Scan(&w.StreamID, &w.Source, &w.SourceLocalKey, &w.CanonicalURL); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (c *ControlStore) UpsertWatch(ctx context.Context, w port.Watch) error {
	_, err := c.pool.Exec(ctx, `
		INSERT INTO control.watch (stream_id, source, source_local_key, canonical_url)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (stream_id) DO UPDATE
		  SET source_local_key = EXCLUDED.source_local_key,
		      canonical_url = EXCLUDED.canonical_url`,
		w.StreamID, w.Source, w.SourceLocalKey, w.CanonicalURL)
	return err
}

// MarkPolled records a poll outcome. On success, next_due_at is the caller's
// normal poll cadence and the failure streak resets. On failure, next_due_at
// (nextDue is ignored) is computed here as an exponential backoff off the
// stream's own consecutive_failures — independent of the 24h poll cadence — so a
// transient error is retried soon while a persistently broken stream still backs
// off, capped at failureBackoffMax rather than waiting a full day.
func (c *ControlStore) MarkPolled(ctx context.Context, streamID string, polledAt, nextDue time.Time, ok bool) error {
	if ok {
		_, err := c.pool.Exec(ctx, `
			INSERT INTO control.poll_state
				(stream_id, last_polled_at, next_due_at, backoff_until, consecutive_failures)
			VALUES ($1, $2, $3, NULL, 0)
			ON CONFLICT (stream_id) DO UPDATE SET
				last_polled_at = $2,
				next_due_at = $3,
				backoff_until = NULL,
				consecutive_failures = 0`,
			streamID, polledAt, nextDue)
		return err
	}

	var failures int
	err := c.pool.QueryRow(ctx,
		`SELECT consecutive_failures FROM control.poll_state WHERE stream_id = $1`, streamID,
	).Scan(&failures)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	failures++
	retryAt := polledAt.Add(failureBackoff(failures))

	_, err = c.pool.Exec(ctx, `
		INSERT INTO control.poll_state
			(stream_id, last_polled_at, next_due_at, backoff_until, consecutive_failures)
		VALUES ($1, $2, $3, $3, $4)
		ON CONFLICT (stream_id) DO UPDATE SET
			last_polled_at = $2,
			next_due_at = $3,
			backoff_until = $3,
			consecutive_failures = $4`,
		streamID, polledAt, retryAt, failures)
	return err
}

// failureBackoff is 1m, 2m, 4m, ... doubling per consecutive failure, capped at
// failureBackoffMax.
func failureBackoff(consecutiveFailures int) time.Duration {
	shift := consecutiveFailures - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 20 { // guard against overflow; failureBackoffMax caps well before this
		shift = 20
	}
	d := failureBackoffBase * time.Duration(int64(1)<<shift)
	if d > failureBackoffMax || d <= 0 {
		d = failureBackoffMax
	}
	return d
}

// MarkPending schedules a soon re-poll for a Resource that exists but whose
// snapshot is not published yet (e-Gov content-publishing lag). It does NOT
// touch consecutive_failures — a content lag is not a failure — and clears any
// backoff so the next_due_at (retryAt) alone gates the re-poll.
func (c *ControlStore) MarkPending(ctx context.Context, streamID string, polledAt, retryAt time.Time) error {
	_, err := c.pool.Exec(ctx, `
		INSERT INTO control.poll_state
			(stream_id, last_polled_at, next_due_at, backoff_until, consecutive_failures)
		VALUES ($1, $2, $3, NULL, 0)
		ON CONFLICT (stream_id) DO UPDATE SET
			last_polled_at = EXCLUDED.last_polled_at,
			next_due_at = EXCLUDED.next_due_at,
			backoff_until = NULL`,
		streamID, polledAt, retryAt)
	return err
}

func (c *ControlStore) Heartbeat(ctx context.Context, source string, at time.Time) error {
	_, err := c.pool.Exec(ctx, `
		INSERT INTO control.daemon_heartbeat (source, beat_at)
		VALUES ($1, $2)
		ON CONFLICT (source) DO UPDATE SET beat_at = EXCLUDED.beat_at`,
		source, at)
	return err
}

func (c *ControlStore) LastHeartbeat(ctx context.Context, source string) (time.Time, error) {
	var beatAt time.Time
	err := c.pool.QueryRow(ctx,
		`SELECT beat_at FROM control.daemon_heartbeat WHERE source = $1`, source,
	).Scan(&beatAt)
	return beatAt, err
}
