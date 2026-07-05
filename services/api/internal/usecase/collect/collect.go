// Package collect is the command-side usecase: fetch a watched Resource, decide
// what changed, and append the resulting observation event. It depends only on
// ports — no HTTP, no SQL, no clock of its own — so the whole pipeline is unit
// testable with fakes.
package collect

import (
	"context"
	"errors"
	"fmt"
	"time"

	obs "s4rciv.org/api/internal/domain/observation"
	"s4rciv.org/api/internal/port"
)

type Collector struct {
	log     port.EventLog
	fetcher port.ResourceFetcher
	control port.ControlStore
	lister  port.MeetingLister
	clock   port.Clock
	ids     port.IDGenerator

	fetcherVersion string
	pollCadence    time.Duration // how long until a stream is polled again
	pendingRetry   time.Duration // shorter re-poll when content is not published yet
	source         string        // the source this collector discovers/upserts watches for
}

type Config struct {
	FetcherVersion string
	PollCadence    time.Duration
	PendingRetry   time.Duration // re-poll delay for ContentUnavailable (default 3h)
	Source         string        // source id stamped on discovered watches (default "kokkai")
}

func New(
	log port.EventLog, fetcher port.ResourceFetcher, control port.ControlStore,
	lister port.MeetingLister, clock port.Clock, ids port.IDGenerator, cfg Config,
) *Collector {
	if cfg.PollCadence <= 0 {
		cfg.PollCadence = 24 * time.Hour
	}
	if cfg.PendingRetry <= 0 {
		cfg.PendingRetry = 3 * time.Hour
	}
	if cfg.Source == "" {
		cfg.Source = "kokkai"
	}
	return &Collector{
		log: log, fetcher: fetcher, control: control, lister: lister,
		clock: clock, ids: ids,
		fetcherVersion: cfg.FetcherVersion, pollCadence: cfg.PollCadence,
		pendingRetry: cfg.PendingRetry, source: cfg.Source,
	}
}

// PollOnce polls every currently-due watch for a source, serially (DISCIPLINE
// §1). Returns how many observation events were emitted. A per-stream failure
// never stops the batch — it is recorded (backoff) and joined into the returned
// error so the caller can log it, while emitted still reflects every stream that
// did succeed.
func (c *Collector) PollOnce(ctx context.Context, source string, limit int) (int, error) {
	due, err := c.control.DueWatches(ctx, source, c.clock.Now(), limit)
	if err != nil {
		return 0, fmt.Errorf("due watches: %w", err)
	}
	emitted := 0
	var errs []error
	for _, w := range due {
		ok, err := c.PollStream(ctx, w)
		if err != nil {
			errs = append(errs, fmt.Errorf("poll %s: %w", w.StreamID, err))
			// Record the failure cursor (backoff) and keep going; one bad stream
			// must not stall the rest. A write failure here is itself worth
			// surfacing: silently swallowing it would leave a persistent
			// control-plane write failure with no trail (the stream would otherwise
			// appear immediately due again next cycle with no clue why).
			if merr := c.control.MarkPolled(ctx, w.StreamID, c.clock.Now(), c.nextDue(), false); merr != nil {
				errs = append(errs, fmt.Errorf("mark failure %s: %w", w.StreamID, merr))
			}
			continue
		}
		if ok {
			emitted++
		}
	}
	return emitted, errors.Join(errs...)
}

// PollStream fetches one Resource and appends an event iff something changed.
// Returns true when an event was emitted.
func (c *Collector) PollStream(ctx context.Context, w port.Watch) (bool, error) {
	if err := c.log.EnsureStream(ctx, port.Stream{
		StreamID: w.StreamID, Source: w.Source,
		SourceLocalKey: w.SourceLocalKey, CanonicalURL: w.CanonicalURL,
	}); err != nil {
		return false, fmt.Errorf("ensure stream: %w", err)
	}

	res, err := c.fetcher.Fetch(ctx, w)
	if err != nil {
		return false, fmt.Errorf("fetch: %w", err)
	}

	if res.ContentUnavailable {
		// The Resource still exists at the source but no snapshot is published yet
		// (e.g. e-Gov switched a law's current-revision pointer before publishing
		// that revision's 法令標準XML). Emit nothing and re-poll soon — never feed
		// Decide an absence it would misread as ResourceVanished (DISCIPLINE §4-3:
		// don't write an absence that did not happen into the immutable log).
		return false, c.control.MarkPending(ctx, w.StreamID, c.clock.Now(), c.pendingRetryAt())
	}

	state, err := c.log.StreamState(ctx, w.StreamID)
	if err != nil {
		return false, fmt.Errorf("stream state: %w", err)
	}

	var observed *obs.Digest
	if res.Present && res.Snapshot != nil {
		observed = &res.Snapshot.ContentHash
	}

	t := obs.Decide(state, observed)
	if !t.Emit {
		return false, c.control.MarkPolled(ctx, w.StreamID, c.clock.Now(), c.nextDue(), true)
	}

	snap := res.Snapshot
	if t.Type == obs.ResourceVanished {
		snap = nil // vanished carries no content
	}
	cmd := port.AppendCmd{
		Stream:            port.Stream{StreamID: w.StreamID, Source: w.Source, SourceLocalKey: w.SourceLocalKey, CanonicalURL: w.CanonicalURL},
		Type:              t.Type,
		EventID:           c.ids.NewID(),
		Source:            w.Source,
		FetcherVersion:    c.fetcherVersion,
		ObservedAt:        c.clock.Now(),
		SourcePublishedAt: res.SourcePublishedAt,
		Snapshot:          snap,
		PrevContentHash:   t.PrevContentHash,
	}
	if _, err := c.log.Append(ctx, cmd); err != nil {
		return false, fmt.Errorf("append: %w", err)
	}
	return true, c.control.MarkPolled(ctx, w.StreamID, c.clock.Now(), c.nextDue(), true)
}

// Discover traverses the source listing and adds discovered resources to the
// watch list. Returns how many refs were upserted.
func (c *Collector) Discover(ctx context.Context, scope port.ListScope) (int, error) {
	refs, err := c.lister.ListMeetings(ctx, scope)
	if err != nil {
		return 0, fmt.Errorf("list meetings: %w", err)
	}
	for _, r := range refs {
		if err := c.control.UpsertWatch(ctx, port.Watch{
			StreamID: r.StreamID, Source: c.source,
			SourceLocalKey: r.SourceLocalKey, CanonicalURL: r.CanonicalURL,
		}); err != nil {
			return 0, fmt.Errorf("upsert watch %s: %w", r.StreamID, err)
		}
	}
	return len(refs), nil
}

func (c *Collector) nextDue() time.Time { return c.clock.Now().Add(c.pollCadence) }

// pendingRetryAt is the (shorter) next-poll time used when a Resource exists but
// its snapshot is not published yet — captures freshly-amended content promptly.
func (c *Collector) pendingRetryAt() time.Time { return c.clock.Now().Add(c.pendingRetry) }
