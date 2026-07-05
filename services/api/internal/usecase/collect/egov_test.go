package collect

import (
	"context"
	"testing"
	"time"

	leg "s4rciv.org/api/internal/domain/legislative"
	obs "s4rciv.org/api/internal/domain/observation"
	"s4rciv.org/api/internal/port"
)

type fakeLawLister struct{}

func (fakeLawLister) ListLaws(context.Context, port.ListScope, string) ([]port.LawRef, error) {
	return nil, nil
}

func (fakeLawLister) ListUpdated(context.Context, port.ListScope) ([]port.LawRef, error) {
	return nil, nil
}

type fakeRevisionLister struct{ revisions []port.LawRevision }

func (l fakeRevisionLister) ListRevisions(context.Context, string) ([]port.LawRevision, error) {
	return l.revisions, nil
}

// fakeRevisionFetcher serves canned results by revision_id; an unregistered id
// resolves to a gap (Present:false), mirroring egov.Gateway.FetchRevision's 404
// behavior — never an error, never ContentUnavailable.
type fakeRevisionFetcher struct{ results map[string]port.FetchResult }

func (f fakeRevisionFetcher) FetchRevision(_ context.Context, revisionID string) (port.FetchResult, error) {
	if r, ok := f.results[revisionID]; ok {
		return r, nil
	}
	return port.FetchResult{Present: false}, nil
}

func newEgovCollector(log port.EventLog, revisions port.RevisionLister, fetcher port.RevisionFetcher) *EgovCollector {
	return NewEgov(
		log, &fakeFetcher{}, &fakeControl{}, fakeLawLister{}, revisions, fetcher,
		&fakeClock{t: time.Unix(1_700_000_000, 0).UTC()}, &fakeIDs{},
		Config{FetcherVersion: "test/0.1.0"},
	)
}

func TestRecoverRevisions_SkipsAlreadyCaptured(t *testing.T) {
	lawID := "415AC0000000057"
	revID := "415AC0000000057_20240401_506AC0000000010"
	streamID := leg.LawRevisionStreamID(lawID, revID)

	log := newFakeLog()
	// Pre-seed the dedicated stream as already recovered in a prior run.
	log.events[streamID] = []port.AppendCmd{{
		Type:     obs.ResourceObserved,
		Snapshot: &port.Snapshot{ContentHash: obs.SumBytes([]byte("already-recovered"))},
	}}

	c := newEgovCollector(log, fakeRevisionLister{revisions: []port.LawRevision{{RevisionID: revID}}}, fakeRevisionFetcher{})

	fetched, missing, err := c.RecoverRevisions(context.Background(), lawID)
	if err != nil {
		t.Fatalf("RecoverRevisions: %v", err)
	}
	if fetched != 0 || missing != 0 {
		t.Fatalf("fetched=%d missing=%d, want 0/0 (already captured, must not re-fetch)", fetched, missing)
	}
}

func TestRecoverRevisions_AppendsMissingOnDedicatedStream(t *testing.T) {
	lawID := "415AC0000000057"
	revID := "415AC0000000057_20240401_506AC0000000010"
	wantStreamID := leg.LawRevisionStreamID(lawID, revID)

	log := newFakeLog()
	enforcement := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	snap := &port.Snapshot{ContentHash: obs.SumBytes([]byte("revision-body"))}
	fetcher := fakeRevisionFetcher{results: map[string]port.FetchResult{
		revID: {Present: true, Snapshot: snap, Permalink: "https://laws.e-gov.go.jp/law/" + lawID},
	}}
	c := newEgovCollector(log, fakeRevisionLister{revisions: []port.LawRevision{
		{RevisionID: revID, EnforcementDate: &enforcement},
	}}, fetcher)

	fetched, missing, err := c.RecoverRevisions(context.Background(), lawID)
	if err != nil {
		t.Fatalf("RecoverRevisions: %v", err)
	}
	if fetched != 1 || missing != 0 {
		t.Fatalf("fetched=%d missing=%d, want 1/0", fetched, missing)
	}

	got := log.events[wantStreamID]
	if len(got) != 1 {
		t.Fatalf("events on %s = %d, want 1", wantStreamID, len(got))
	}
	if got[0].Type != obs.ResourceObserved {
		t.Fatalf("event type = %v, want ResourceObserved", got[0].Type)
	}
	if got[0].PrevContentHash != nil {
		t.Fatal("recovered revision must be the sole event on its dedicated stream (PrevContentHash nil)")
	}
	if got[0].SourcePublishedAt == nil || !got[0].SourcePublishedAt.Equal(enforcement) {
		t.Fatalf("source_published_at = %v, want %v", got[0].SourcePublishedAt, enforcement)
	}
	// The law's PRIMARY polling stream (used by the recurring poll's StreamState
	// dedup) must be completely untouched by gap-recovery.
	if len(log.events[leg.LawStreamID(lawID)]) != 0 {
		t.Fatal("recovered revision leaked onto the law's primary polling stream")
	}
}

func TestRecoverRevisions_ReportsMissingWithoutAppending(t *testing.T) {
	lawID := "415AC0000000057"
	revID := "415AC0000000057_19990401_000AC0000000001" // never registered -> fetcher reports a gap

	log := newFakeLog()
	c := newEgovCollector(log, fakeRevisionLister{revisions: []port.LawRevision{{RevisionID: revID}}}, fakeRevisionFetcher{})

	fetched, missing, err := c.RecoverRevisions(context.Background(), lawID)
	if err != nil {
		t.Fatalf("RecoverRevisions: %v", err)
	}
	if fetched != 0 || missing != 1 {
		t.Fatalf("fetched=%d missing=%d, want 0/1", fetched, missing)
	}
	streamID := leg.LawRevisionStreamID(lawID, revID)
	if len(log.events[streamID]) != 0 {
		t.Fatal("a missing revision must never be appended (no fabricated observation)")
	}
}
