package port

import (
	"context"
	"time"

	leg "s4rciv.org/api/internal/domain/legislative"
)

// LawRef is a discovered law to add to the watch list (mirrors MeetingRef).
type LawRef struct {
	StreamID       string
	SourceLocalKey string // e-Gov 法令ID
	CanonicalURL   string
}

// LawLister traverses the e-Gov listing endpoints. ListLaws backfills via /laws;
// ListUpdated re-polls via /updatelawlists over the scope's date window.
type LawLister interface {
	ListLaws(ctx context.Context, scope ListScope, lawType string) ([]LawRef, error)
	ListUpdated(ctx context.Context, scope ListScope) ([]LawRef, error)
}

// LawRevision is one entry in a law's amendment history, as enumerated by
// /law_revisions/{law_id} (v2). Used only for gap-recovery (RevisionLister),
// never as the discover signal LawLister.ListUpdated already owns.
type LawRevision struct {
	RevisionID      string     // law_revision_id — the /law_data/{id} path key
	EnforcementDate *time.Time // amendment_enforcement_date; nil if blank/未施行
	PromulgateDate  *time.Time // amendment_promulgate_date; nil if blank
	AmendmentLawID  string     // amendment_law_id — the amending law that produced this revision
}

// RevisionLister enumerates one law's full revision history via
// /law_revisions/{law_id} (v2). Distinct from LawLister: this is a per-law,
// gap-recovery lookup, not corpus-wide discovery.
type RevisionLister interface {
	ListRevisions(ctx context.Context, lawID string) ([]LawRevision, error)
}

// RevisionFetcher fetches one historical revision's byte-exact snapshot by
// revision_id via /law_data/{revision_id} (v2). A 404 here means e-Gov never
// published or no longer retains that specific historical snapshot — it must
// never be resolved through the law-existence oracle (absenceOrPending): the
// law itself may well still exist, only this named past revision is missing.
type RevisionFetcher interface {
	FetchRevision(ctx context.Context, revisionID string) (FetchResult, error)
}

// LawNormalizer is the anti-corruption parse from canonical 法令標準XML bytes to
// the interpretation-plane domain. Pure with respect to a snapshot.
type LawNormalizer interface {
	ParseLaw(content []byte) (leg.LawContent, error)
}

// LawProjectionBatch is the full set of law read-model rows derived from one law
// snapshot. The store writes it in one transaction, replacing the prior projection
// of that law (reproject-safe).
type LawProjectionBatch struct {
	Law            leg.Law
	Nodes          []leg.LawNode
	ObservationSeq int64
	ObservedAt     time.Time
}

// LawReadModelStore writes the disposable law read models (legislative_work + law_node).
type LawReadModelStore interface {
	ApplyLaw(ctx context.Context, b LawProjectionBatch) error
	TruncateLaws(ctx context.Context) error
}

// ChangeRecord is one row to persist into interpretation.change.
type ChangeRecord struct {
	ObservationSeq  int64
	DifferVersion   string
	DiffJSON        []byte // serialized {law_id, node_changes:[...]}
	Classification  string // administrative | substantive
	ClassConfidence string // high | medium | low
}

// ChangeStore writes the diff/classification read model. TruncateEgov clears the
// egov-law change rows for a reproject of the differ projector.
type ChangeStore interface {
	ApplyChange(ctx context.Context, r ChangeRecord) error
	TruncateEgov(ctx context.Context) error
}

// NodeChange is one structural change reported by the differ (shared eId contract).
type NodeChange struct {
	EID      string
	Op       string // added | deleted | modified | moved
	NodeType string
	Num      string
	PrevText string
	CurrText string
}

// DiffResult is the differ's verdict over two consecutive snapshots.
type DiffResult struct {
	DifferVersion   string
	Classification  string
	ClassConfidence string
	NodeChanges     []NodeChange
}

// DiffClient computes a structural change over the Connect-RPC DiffService. The
// structural diff is owned by the Rust differ (ADR-000005); Go owns persistence.
type DiffClient interface {
	ComputeChange(ctx context.Context, prev, curr []byte, streamID, mediaType string) (DiffResult, error)
}

// ── Law query views (read side) ─────────────────────────────────────────────

type LawView struct {
	Law  leg.Law
	Attr Attribution
}

type LawNodeView struct {
	Node leg.LawNode
}

type LawChangeView struct {
	ObservationSeq  int64
	DifferVersion   string
	Classification  string
	ClassConfidence string
	ObservedAt      time.Time
	NodeChanges     []NodeChange
}

// LawQueryReader is the read-only view over the law read models.
type LawQueryReader interface {
	GetLaw(ctx context.Context, lawID string) (LawView, []LawNodeView, bool, error)
	ListLaws(ctx context.Context, lawType string, limit, offset int) ([]LawView, error)
	GetLawChanges(ctx context.Context, lawID string, limit, offset int) ([]LawChangeView, error)
}
