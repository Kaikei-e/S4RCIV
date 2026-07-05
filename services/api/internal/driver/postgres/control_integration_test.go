//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"s4rciv.org/api/internal/port"
)

// insertWatch inserts a control.watch row against the 'kokkai' source, which the
// migrated template already seeds (20260603000005_kokkai_source_seed.sql).
func insertWatch(t *testing.T, ctx context.Context, store *ControlStore, streamID string) {
	t.Helper()
	if err := store.UpsertWatch(ctx, port.Watch{
		StreamID: streamID, Source: "kokkai", SourceLocalKey: streamID, CanonicalURL: "https://example.test/" + streamID,
	}); err != nil {
		t.Fatalf("insert watch %s: %v", streamID, err)
	}
}

func TestMarkPolled_FailureBacksOffExponentially_SuccessResets(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newTestDB(t)
	store := NewControlStore(pool)
	insertWatch(t, ctx, store, "kokkai:CTRL1")

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// First failure: consecutive_failures 0 -> 1, backoff = polledAt + 1m (base unit).
	if err := store.MarkPolled(ctx, "kokkai:CTRL1", base, base.Add(24*time.Hour), false); err != nil {
		t.Fatalf("MarkPolled (1st failure): %v", err)
	}
	var nextDue, backoffUntil time.Time
	var failures int
	if err := pool.QueryRow(ctx,
		`SELECT next_due_at, backoff_until, consecutive_failures FROM control.poll_state WHERE stream_id = $1`,
		"kokkai:CTRL1",
	).Scan(&nextDue, &backoffUntil, &failures); err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("consecutive_failures = %d, want 1", failures)
	}
	want1 := base.Add(1 * time.Minute)
	if !nextDue.Equal(want1) || !backoffUntil.Equal(want1) {
		t.Fatalf("after 1st failure next_due_at=%v backoff_until=%v, want both %v (NOT the 24h cadence)", nextDue, backoffUntil, want1)
	}

	// Second failure: streak 1 -> 2, backoff doubles to polledAt + 2m.
	base2 := base.Add(90 * time.Second) // arbitrary later "now"; MarkPolled trusts the caller's clock
	if err := store.MarkPolled(ctx, "kokkai:CTRL1", base2, base2.Add(24*time.Hour), false); err != nil {
		t.Fatalf("MarkPolled (2nd failure): %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT next_due_at, backoff_until, consecutive_failures FROM control.poll_state WHERE stream_id = $1`,
		"kokkai:CTRL1",
	).Scan(&nextDue, &backoffUntil, &failures); err != nil {
		t.Fatal(err)
	}
	if failures != 2 {
		t.Fatalf("consecutive_failures = %d, want 2", failures)
	}
	want2 := base2.Add(2 * time.Minute)
	if !nextDue.Equal(want2) || !backoffUntil.Equal(want2) {
		t.Fatalf("after 2nd failure next_due_at=%v backoff_until=%v, want both %v (doubled)", nextDue, backoffUntil, want2)
	}

	// A due-watches scan between failures must exclude the stream until its
	// backoff passes.
	due, err := store.DueWatches(ctx, "kokkai", base2.Add(1*time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range due {
		if w.StreamID == "kokkai:CTRL1" {
			t.Fatalf("stream still in backoff must not be due at %v (backoff_until=%v)", base2.Add(1*time.Minute), want2)
		}
	}
	due, err = store.DueWatches(ctx, "kokkai", want2.Add(time.Second), 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range due {
		if w.StreamID == "kokkai:CTRL1" {
			found = true
		}
	}
	if !found {
		t.Fatal("stream must be due once its backoff_until has passed")
	}

	// Success resets the streak and clears backoff, using the caller's cadence
	// (not the failure backoff schedule).
	base3 := base2.Add(5 * time.Minute)
	cadence := base3.Add(24 * time.Hour)
	if err := store.MarkPolled(ctx, "kokkai:CTRL1", base3, cadence, true); err != nil {
		t.Fatalf("MarkPolled (success): %v", err)
	}
	var backoffNull *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT next_due_at, backoff_until, consecutive_failures FROM control.poll_state WHERE stream_id = $1`,
		"kokkai:CTRL1",
	).Scan(&nextDue, &backoffNull, &failures); err != nil {
		t.Fatal(err)
	}
	if failures != 0 {
		t.Fatalf("consecutive_failures after success = %d, want 0 (reset)", failures)
	}
	if backoffNull != nil {
		t.Fatalf("backoff_until after success = %v, want NULL (cleared)", *backoffNull)
	}
	if !nextDue.Equal(cadence) {
		t.Fatalf("next_due_at after success = %v, want the caller's cadence %v", nextDue, cadence)
	}
}

func TestHeartbeat_RoundTrips_LatestWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newTestDB(t)
	store := NewControlStore(pool)

	if _, err := store.LastHeartbeat(ctx, "kokkai"); err == nil {
		t.Fatal("LastHeartbeat before any beat should error (no row)")
	}

	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.Heartbeat(ctx, "kokkai", t1); err != nil {
		t.Fatal(err)
	}
	got, err := store.LastHeartbeat(ctx, "kokkai")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(t1) {
		t.Fatalf("LastHeartbeat = %v, want %v", got, t1)
	}

	t2 := t1.Add(time.Minute)
	if err := store.Heartbeat(ctx, "kokkai", t2); err != nil {
		t.Fatal(err)
	}
	got, err = store.LastHeartbeat(ctx, "kokkai")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(t2) {
		t.Fatalf("LastHeartbeat after 2nd beat = %v, want the latest %v (upsert, not append)", got, t2)
	}
}
