package collect

import (
	"context"
	"fmt"

	leg "s4rciv.org/api/internal/domain/legislative"
	obs "s4rciv.org/api/internal/domain/observation"
	"s4rciv.org/api/internal/gateway/egov"
	"s4rciv.org/api/internal/port"
)

// EgovCollector is the command-side collector for the egov-law source. It reuses
// the generic poll path (EnsureStream -> Fetch -> Decide -> Append -> MarkPolled)
// via an embedded Collector and only differs in discovery: the e-Gov 法令一覧
// (backfill) and 更新法令一覧 (re-poll) instead of kokkai's meeting_list. It
// additionally exposes RecoverRevisions, a gap-recovery path independent of the
// recurring poll/discover flow (v2 law_revisions + law_data/{revision_id}).
type EgovCollector struct {
	*Collector
	control         port.ControlStore
	lister          port.LawLister
	revisions       port.RevisionLister
	revisionFetcher port.RevisionFetcher
}

func NewEgov(
	log port.EventLog, fetcher port.ResourceFetcher, control port.ControlStore,
	lister port.LawLister, revisions port.RevisionLister, revisionFetcher port.RevisionFetcher,
	clock port.Clock, ids port.IDGenerator, cfg Config,
) *EgovCollector {
	base := New(log, fetcher, control, nil, clock, ids, cfg)
	return &EgovCollector{
		Collector: base, control: control, lister: lister,
		revisions: revisions, revisionFetcher: revisionFetcher,
	}
}

// Discover backfills the watch list from /laws (optionally filtered by law_type).
func (c *EgovCollector) Discover(ctx context.Context, scope port.ListScope, lawType string) (int, error) {
	refs, err := c.lister.ListLaws(ctx, scope, lawType)
	if err != nil {
		return 0, fmt.Errorf("list laws: %w", err)
	}
	return c.upsert(ctx, refs)
}

// DiscoverUpdated adds laws updated within the scope window (from 更新法令一覧).
func (c *EgovCollector) DiscoverUpdated(ctx context.Context, scope port.ListScope) (int, error) {
	refs, err := c.lister.ListUpdated(ctx, scope)
	if err != nil {
		return 0, fmt.Errorf("list updated laws: %w", err)
	}
	return c.upsert(ctx, refs)
}

// RecoverRevisions enumerates one law's full revision history and appends any
// revision not yet captured, each on its own dedicated stream
// (leg.LawRevisionStreamID) — never the law's primary polling stream, whose
// StreamState (current-content dedup for the next real poll) must not be
// disturbed by a recovered past snapshot. A revision RevisionFetcher cannot
// retrieve is counted as missing and reported to the caller, never synthesized
// (DISCIPLINE §4-3: don't write an absence/gap that wasn't actually observed).
//
// This is independent of PollStream/Discover/DiscoverUpdated: it is invoked
// explicitly (recover-revisions CLI subcommand), not from run/poll-once/discover,
// so it never multiplies the recurring poll's request volume.
func (c *EgovCollector) RecoverRevisions(ctx context.Context, lawID string) (fetched, missing int, err error) {
	revisions, err := c.revisions.ListRevisions(ctx, lawID)
	if err != nil {
		return 0, 0, fmt.Errorf("list revisions %s: %w", lawID, err)
	}
	for _, r := range revisions {
		if r.RevisionID == "" {
			continue
		}
		streamID := leg.LawRevisionStreamID(lawID, r.RevisionID)
		state, err := c.log.StreamState(ctx, streamID)
		if err != nil {
			return fetched, missing, fmt.Errorf("stream state %s: %w", streamID, err)
		}
		if state.Exists {
			continue // already recovered in a prior run
		}

		res, err := c.revisionFetcher.FetchRevision(ctx, r.RevisionID)
		if err != nil {
			return fetched, missing, fmt.Errorf("fetch revision %s: %w", r.RevisionID, err)
		}
		if !res.Present {
			missing++
			continue
		}

		stream := port.Stream{
			StreamID: streamID, Source: egov.SourceName,
			SourceLocalKey: r.RevisionID, CanonicalURL: res.Permalink,
		}
		if err := c.log.EnsureStream(ctx, stream); err != nil {
			return fetched, missing, fmt.Errorf("ensure stream %s: %w", streamID, err)
		}
		cmd := port.AppendCmd{
			Stream:            stream,
			Type:              obs.ResourceObserved,
			EventID:           c.ids.NewID(),
			Source:            egov.SourceName,
			FetcherVersion:    c.fetcherVersion,
			ObservedAt:        c.clock.Now(),
			SourcePublishedAt: r.EnforcementDate,
			Snapshot:          res.Snapshot,
			PrevContentHash:   nil, // fresh stream's first (only) event
		}
		if _, err := c.log.Append(ctx, cmd); err != nil {
			return fetched, missing, fmt.Errorf("append revision %s: %w", r.RevisionID, err)
		}
		fetched++
	}
	return fetched, missing, nil
}

func (c *EgovCollector) upsert(ctx context.Context, refs []port.LawRef) (int, error) {
	for _, r := range refs {
		if err := c.control.UpsertWatch(ctx, port.Watch{
			StreamID: r.StreamID, Source: egov.SourceName,
			SourceLocalKey: r.SourceLocalKey, CanonicalURL: r.CanonicalURL,
		}); err != nil {
			return 0, fmt.Errorf("upsert watch %s: %w", r.StreamID, err)
		}
	}
	return len(refs), nil
}
