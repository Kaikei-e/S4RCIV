// Command collector is the command side: it discovers and polls public-source
// resources over a read-only, rate-limited HTTP boundary, appends observation
// events, and projects the interpretation read models. The source is selected by
// --source (kokkai | egov-law; default kokkai). Subcommands:
//
//	collector [--source S] run                     daemon: poll due watches + project, on a loop
//	collector [--source S] poll-once               one poll+project cycle, then exit
//	collector [--source S] reproject               truncate read models and replay from seq 0
//	collector [--source S] discover --from --until seed the watch list from the source listing
//
// For egov-law, discover also accepts --law-type and --updated (use 更新法令一覧),
// and a cycle additionally runs the differ usecase (interpretation.change).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"s4rciv.org/api/internal/driver/diffrpc"
	"s4rciv.org/api/internal/driver/egovhttp"
	"s4rciv.org/api/internal/driver/kokkaihttp"
	"s4rciv.org/api/internal/driver/postgres"
	"s4rciv.org/api/internal/driver/signer"
	"s4rciv.org/api/internal/driver/sys"
	"s4rciv.org/api/internal/gateway/egov"
	"s4rciv.org/api/internal/gateway/giinroster"
	"s4rciv.org/api/internal/gateway/kokkai"
	"s4rciv.org/api/internal/gateway/sangiin"
	"s4rciv.org/api/internal/port"
	"s4rciv.org/api/internal/usecase/checkpoint"
	"s4rciv.org/api/internal/usecase/collect"
	"s4rciv.org/api/internal/usecase/diff"
	"s4rciv.org/api/internal/usecase/project"
)

const (
	collectorVersion = "S4rCiv-collect/0.1.0"
	daemonInterval   = 60 * time.Second
	pollBatch        = 100
	// projectInterval bounds read-model latency independently of polling: the
	// projector runs on this tick (catch-up phase) plus on an in-process signal
	// after a poll emits events (live phase), so projection is never held hostage
	// to a long poll batch (ADR-000015; catch-up-subscription pattern).
	projectInterval = 10 * time.Second
	// discoverInterval is how often the daemon refreshes the watch list in-process
	// (ADR-000012). Much coarser than daemonInterval: new resources appear daily at
	// most and source records carry a publish lag.
	discoverInterval = 24 * time.Hour
	// discoverWindowDays is the rolling look-back (by resource date) for date-windowed
	// sources (kokkai meeting_list, egov 更新法令一覧). Wide enough to re-scan late-
	// published records and absorb a multi-week daemon outage; upsert is idempotent so
	// re-scanning is cheap and safe (ADR-000012). Long gaps still need a manual catch-up.
	discoverWindowDays = 90
	// autoDiscoverMax caps how many refs one auto-discover cycle may collect, so a
	// misbehaving upstream listing cannot balloon the watch list unattended. The
	// 90-day window holds well under this in practice; a deliberate backfill beyond
	// it goes through the manual discover command.
	autoDiscoverMax = 2000
	// heartbeatStaleAfter bounds how long a daemon may go without reaching the top
	// of its poll loop before an external healthcheck calls it wedged. Comfortably
	// above maxThrottleBackoff (egovhttp/kokkaihttp: 15m) so a single legitimate
	// 429/503 backoff episode never trips a false positive.
	heartbeatStaleAfter = 20 * time.Minute
)

// recentScope is the rolling discover window [today-discoverWindowDays, today],
// inclusive, in the YYYY-MM-DD form the date-windowed listers expect.
func recentScope(now time.Time) port.ListScope {
	return port.ListScope{
		From:  now.AddDate(0, 0, -discoverWindowDays).Format("2006-01-02"),
		Until: now.Format("2006-01-02"),
		Max:   autoDiscoverMax,
	}
}

// logDiscover reports an auto-discover outcome without ever being fatal (the
// daemon must keep polling even if a discover cycle fails).
func logDiscover(source string, n int, err error) {
	if err != nil {
		log.Printf("auto-discover %s: %v", source, err)
		return
	}
	log.Printf("auto-discover %s: upserted %d watches", source, n)
}

// pipeline abstracts the per-source command side. poll (command-in) and project
// (read-model build) are separate so the daemon can run them on independent loops
// (ADR-000015): a long poll batch must not block read-model projection.
type pipeline interface {
	source() string
	// poll fetches due watches and appends observation events; returns how many
	// events were emitted (used to signal the project loop's live phase).
	poll(ctx context.Context) int
	// project folds new observation events into the read models [+ diffs]. Safe to
	// run on its own cadence: the projector is checkpoint-based and idempotent.
	project(ctx context.Context)
	reproject(ctx context.Context) error
	discover(ctx context.Context, args []string)
	// autoDiscover refreshes the watch list in-daemon so new resources are
	// followed without a manual discover (ADR-000012). It runs in the same
	// process as cycle(), sharing the source's serial rate limiter (DISCIPLINE
	// §1), so it never needs the daemon paused. It must not be fatal: a discover
	// error is logged and the daemon keeps polling.
	autoDiscover(ctx context.Context)
}

func main() {
	fs := flag.NewFlagSet("collector", flag.ExitOnError)
	source := fs.String("source", "kokkai", "source to operate on (kokkai | egov-law | giin-roster | sangiin-vote | sangiin-roster)")
	healthcheck := fs.Bool("healthcheck", false, "probe this source's daemon heartbeat and exit (for the container healthcheck)")
	_ = fs.Parse(os.Args[1:])
	if *healthcheck {
		os.Exit(probeHeartbeat(*source))
	}
	rest := fs.Args()
	if len(rest) < 1 {
		usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.Connect(ctx)
	if err != nil {
		log.Fatalf("startup: %v", err)
	}
	defer pool.Close()

	// checkpoint attests the global log-chain head, so it is source-agnostic and does
	// not need a per-source pipeline.
	if rest[0] == "checkpoint" {
		runCheckpoint(ctx, pool, rest[1:])
		return
	}

	p, cfg, control, err := wire(ctx, pool, *source)
	if err != nil {
		log.Fatalf("wire %s: %v", *source, err)
	}

	// control.source.enabled is the operator's kill switch for one source: a
	// disabled source must not be polled or discovered. reproject stays available —
	// it only replays already-recorded events and never touches the source.
	if !cfg.Enabled && commandNeedsEnabledSource(rest[0]) {
		log.Printf("source %s is disabled in control.source; refusing %q (set enabled=true to resume)", *source, rest[0])
		return
	}

	switch rest[0] {
	case "run":
		runDaemon(ctx, p, *source, control)
	case "poll-once":
		p.poll(ctx)
		p.project(ctx)
	case "reproject":
		if err := p.reproject(ctx); err != nil {
			log.Fatalf("reproject: %v", err)
		}
	case "discover":
		p.discover(ctx, rest[1:])
	default:
		usage()
		os.Exit(2)
	}
}

// commandNeedsEnabledSource reports whether the subcommand contacts the source
// (run/poll/discover) and therefore must be refused when control.source.enabled
// is false. reproject only replays recorded observation events, so it is exempt.
func commandNeedsEnabledSource(cmd string) bool {
	switch cmd {
	case "run", "poll-once", "discover":
		return true
	}
	return false
}

func wire(ctx context.Context, pool *pgxpool.Pool, source string) (pipeline, port.SourceConfig, port.ControlStore, error) {
	control := postgres.NewControlStore(pool)
	cfg, err := control.Source(ctx, source)
	if err != nil {
		return nil, cfg, control, fmt.Errorf("load source %q: %w", source, err)
	}
	ua := envOr("USER_AGENT", cfg.UserAgent)

	switch source {
	case kokkai.SourceName:
		httpc, err := kokkaihttp.New(cfg.BaseURL, ua, cfg.RateLimit)
		if err != nil {
			return nil, cfg, control, err
		}
		gw := kokkai.New(httpc)
		collector := collect.New(
			postgres.NewEventLog(pool), gw, control, gw, sys.Clock{}, sys.IDGen{},
			collect.Config{FetcherVersion: collectorVersion, Source: kokkai.SourceName},
		)
		rm := postgres.NewReadModel(pool)
		projector := project.New(postgres.NewEventReader(pool), gw, rm, rm, source)
		return &kokkaiPipeline{collector: collector, projector: projector}, cfg, control, nil

	case giinroster.SourceName:
		// egovhttp is a generic rate-limited + robots-compliant GET client (GetAbs);
		// reused here for the roster pages (ADR-000008). DB is Go-owned; differ N/A.
		// The 両院 roster spans www.shugiin.go.jp (base) AND www.sangiin.go.jp, so the
		// 参 host is added to the GetAbs allowlist (SSRF guard; F-005).
		httpc, err := egovhttp.New(cfg.BaseURL, ua, cfg.RateLimit, "www.sangiin.go.jp")
		if err != nil {
			return nil, cfg, control, err
		}
		gw := giinroster.New(httpc)
		collector := collect.New(
			postgres.NewEventLog(pool), gw, control, gw, sys.Clock{}, sys.IDGen{},
			collect.Config{FetcherVersion: collectorVersion, Source: giinroster.SourceName},
		)
		rm := postgres.NewRosterReadModel(pool, giinroster.StreamID(""))
		projector := project.NewRoster(postgres.NewEventReader(pool), gw, rm, rm, giinroster.SourceName, giinroster.StreamID(""))
		return &giinRosterPipeline{collector: collector, projector: projector}, cfg, control, nil

	case sangiin.SourceName: // 参議院本会議投票結果 (touhyoulist) — per-member roll-calls (ADR-000010)
		httpc, err := egovhttp.New(cfg.BaseURL, ua, cfg.RateLimit)
		if err != nil {
			return nil, cfg, control, err
		}
		gw := sangiin.New(httpc)
		collector := collect.New(
			postgres.NewEventLog(pool), gw, control, noLister{}, sys.Clock{}, sys.IDGen{},
			collect.Config{FetcherVersion: collectorVersion, Source: sangiin.SourceName},
		)
		rm := postgres.NewSangiinVoteReadModel(pool)
		projector := project.NewSangiinVote(postgres.NewEventReader(pool), gw, rm, rm, sangiin.SourceName)
		return &sangiinVotePipeline{collector: collector, projector: projector, gw: gw, control: control}, cfg, control, nil

	case sangiin.RosterSourceName: // 参議院議員名簿 → legislator_district (house=参議院)
		httpc, err := egovhttp.New(cfg.BaseURL, ua, cfg.RateLimit)
		if err != nil {
			return nil, cfg, control, err
		}
		gw := sangiin.New(httpc)
		collector := collect.New(
			postgres.NewEventLog(pool), gw, control, noLister{}, sys.Clock{}, sys.IDGen{},
			collect.Config{FetcherVersion: collectorVersion, Source: sangiin.RosterSourceName},
		)
		rm := postgres.NewRosterReadModel(pool, sangiin.RosterStreamID(""))
		projector := project.NewRoster(postgres.NewEventReader(pool), gw, rm, rm, sangiin.RosterSourceName, sangiin.RosterStreamID(""))
		return &sangiinRosterPipeline{collector: collector, projector: projector, gw: gw, control: control}, cfg, control, nil

	case egov.SourceName:
		httpc, err := egovhttp.New(cfg.BaseURL, ua, cfg.RateLimit)
		if err != nil {
			return nil, cfg, control, err
		}
		gw := egov.New(httpc)
		collector := collect.NewEgov(
			postgres.NewEventLog(pool), gw, control, gw, sys.Clock{}, sys.IDGen{},
			collect.Config{FetcherVersion: collectorVersion},
		)
		reader := postgres.NewEventReader(pool)
		lawRM := postgres.NewLawReadModel(pool)
		projector := project.NewLaw(reader, gw, lawRM, lawRM, source)
		changeRM := postgres.NewChangeReadModel(pool)
		differ := diff.New(reader, diffrpc.New(envOr("DIFFER_URL", "http://differ:9090")), changeRM, changeRM, "egov-differ")
		return &egovPipeline{collector: collector, projector: projector, differ: differ}, cfg, control, nil

	default:
		return nil, cfg, control, fmt.Errorf("unknown source %q", source)
	}
}

// ── kokkai pipeline ─────────────────────────────────────────────────────────

type kokkaiPipeline struct {
	collector *collect.Collector
	projector *project.Projector
}

func (k *kokkaiPipeline) source() string { return kokkai.SourceName }

func (k *kokkaiPipeline) poll(ctx context.Context) int {
	emitted, err := k.collector.PollOnce(ctx, kokkai.SourceName, pollBatch)
	if err != nil {
		log.Printf("poll: %v", err)
	}
	if emitted > 0 {
		log.Printf("poll: emitted %d events", emitted)
	}
	return emitted
}

func (k *kokkaiPipeline) project(ctx context.Context) {
	projected, err := k.projector.Run(ctx)
	if err != nil {
		log.Printf("project: %v", err)
		return
	}
	if projected > 0 {
		log.Printf("project: %d meetings", projected)
	}
}

func (k *kokkaiPipeline) reproject(ctx context.Context) error {
	n, err := k.projector.Reproject(ctx)
	if err != nil {
		return err
	}
	log.Printf("reproject: projected %d meetings", n)
	return nil
}

func (k *kokkaiPipeline) discover(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	from := fs.String("from", "", "start date YYYY-MM-DD (inclusive)")
	until := fs.String("until", "", "end date YYYY-MM-DD (inclusive)")
	max := fs.Int("max", 0, "cap on discovered resources (0 = no cap)")
	_ = fs.Parse(args)
	if *from == "" || *until == "" {
		log.Fatal("discover requires --from and --until (YYYY-MM-DD)")
	}
	n, err := k.collector.Discover(ctx, port.ListScope{From: *from, Until: *until, Max: *max})
	if err != nil {
		log.Fatalf("discover: %v", err)
	}
	log.Printf("discover: upserted %d watches", n)
}

func (k *kokkaiPipeline) autoDiscover(ctx context.Context) {
	n, err := k.collector.Discover(ctx, recentScope(time.Now()))
	logDiscover(kokkai.SourceName, n, err)
}

// ── giin-roster pipeline ────────────────────────────────────────────────────

type giinRosterPipeline struct {
	collector *collect.Collector
	projector *project.RosterProjector
}

func (g *giinRosterPipeline) source() string { return giinroster.SourceName }

func (g *giinRosterPipeline) poll(ctx context.Context) int {
	emitted, err := g.collector.PollOnce(ctx, giinroster.SourceName, pollBatch)
	if err != nil {
		log.Printf("poll: %v", err)
	}
	if emitted > 0 {
		log.Printf("poll: emitted %d events", emitted)
	}
	return emitted
}

func (g *giinRosterPipeline) project(ctx context.Context) {
	projected, err := g.projector.Run(ctx)
	if err != nil {
		log.Printf("project: %v", err)
		return
	}
	if projected > 0 {
		log.Printf("project: %d roster pages", projected)
	}
}

func (g *giinRosterPipeline) reproject(ctx context.Context) error {
	n, err := g.projector.Reproject(ctx)
	if err != nil {
		return err
	}
	log.Printf("reproject: projected %d roster pages", n)
	return nil
}

func (g *giinRosterPipeline) discover(ctx context.Context, _ []string) {
	// The roster is a fixed page set, so discover ignores --from/--until.
	n, err := g.collector.Discover(ctx, port.ListScope{})
	if err != nil {
		log.Fatalf("discover: %v", err)
	}
	log.Printf("discover: upserted %d watches", n)
}

func (g *giinRosterPipeline) autoDiscover(ctx context.Context) {
	// Fixed page set; re-discover daily to catch roster changes (e.g. after an election).
	n, err := g.collector.Discover(ctx, port.ListScope{})
	logDiscover(giinroster.SourceName, n, err)
}

// ── 参議院 pipelines (vote + roster) ─────────────────────────────────────────

// noLister satisfies the Collector's MeetingLister dependency for sources whose
// discovery is bespoke (sangiin) and never goes through collect.Discover.
type noLister struct{}

func (noLister) ListMeetings(context.Context, port.ListScope) ([]port.MeetingRef, error) {
	return nil, nil
}

type sangiinVotePipeline struct {
	collector *collect.Collector
	projector *project.SangiinVoteProjector
	gw        *sangiin.Gateway
	control   port.ControlStore
}

func (p *sangiinVotePipeline) source() string { return sangiin.SourceName }

func (p *sangiinVotePipeline) poll(ctx context.Context) int {
	emitted, err := p.collector.PollOnce(ctx, sangiin.SourceName, pollBatch)
	if err != nil {
		log.Printf("poll: %v", err)
	}
	if emitted > 0 {
		log.Printf("poll: emitted %d events", emitted)
	}
	return emitted
}

func (p *sangiinVotePipeline) project(ctx context.Context) {
	projected, err := p.projector.Run(ctx)
	if err != nil {
		log.Printf("project: %v", err)
		return
	}
	if projected > 0 {
		log.Printf("project: %d vote pages", projected)
	}
}

func (p *sangiinVotePipeline) reproject(ctx context.Context) error {
	n, err := p.projector.Reproject(ctx)
	if err != nil {
		return err
	}
	log.Printf("reproject: projected %d vote pages", n)
	return nil
}

func (p *sangiinVotePipeline) discover(ctx context.Context, _ []string) {
	refs, err := p.gw.DiscoverVotes(ctx)
	if err != nil {
		log.Fatalf("discover: %v", err)
	}
	for _, r := range refs {
		if err := p.control.UpsertWatch(ctx, port.Watch{
			StreamID: r.StreamID, Source: sangiin.SourceName,
			SourceLocalKey: r.SourceLocalKey, CanonicalURL: r.CanonicalURL,
		}); err != nil {
			log.Fatalf("upsert watch %q: %v", r.StreamID, err)
		}
	}
	log.Printf("discover: upserted %d watches", len(refs))
}

func (p *sangiinVotePipeline) autoDiscover(ctx context.Context) {
	// 記名投票 accrues within a session; re-discover daily so new roll-calls are followed.
	refs, err := p.gw.DiscoverVotes(ctx)
	if err != nil {
		logDiscover(sangiin.SourceName, 0, err)
		return
	}
	for _, r := range refs {
		if err := p.control.UpsertWatch(ctx, port.Watch{
			StreamID: r.StreamID, Source: sangiin.SourceName,
			SourceLocalKey: r.SourceLocalKey, CanonicalURL: r.CanonicalURL,
		}); err != nil {
			logDiscover(sangiin.SourceName, 0, fmt.Errorf("upsert %q: %w", r.StreamID, err))
			return
		}
	}
	logDiscover(sangiin.SourceName, len(refs), nil)
}

type sangiinRosterPipeline struct {
	collector *collect.Collector
	projector *project.RosterProjector
	gw        *sangiin.Gateway
	control   port.ControlStore
}

func (p *sangiinRosterPipeline) source() string { return sangiin.RosterSourceName }

func (p *sangiinRosterPipeline) poll(ctx context.Context) int {
	emitted, err := p.collector.PollOnce(ctx, sangiin.RosterSourceName, pollBatch)
	if err != nil {
		log.Printf("poll: %v", err)
	}
	if emitted > 0 {
		log.Printf("poll: emitted %d events", emitted)
	}
	return emitted
}

func (p *sangiinRosterPipeline) project(ctx context.Context) {
	projected, err := p.projector.Run(ctx)
	if err != nil {
		log.Printf("project: %v", err)
		return
	}
	if projected > 0 {
		log.Printf("project: %d roster pages", projected)
	}
}

func (p *sangiinRosterPipeline) reproject(ctx context.Context) error {
	n, err := p.projector.Reproject(ctx)
	if err != nil {
		return err
	}
	log.Printf("reproject: projected %d roster pages", n)
	return nil
}

func (p *sangiinRosterPipeline) discover(ctx context.Context, _ []string) {
	ref, err := p.gw.DiscoverRoster(ctx)
	if err != nil {
		log.Fatalf("discover: %v", err)
	}
	if err := p.control.UpsertWatch(ctx, port.Watch{
		StreamID: ref.StreamID, Source: sangiin.RosterSourceName,
		SourceLocalKey: ref.SourceLocalKey, CanonicalURL: ref.CanonicalURL,
	}); err != nil {
		log.Fatalf("upsert watch: %v", err)
	}
	log.Printf("discover: upserted 1 watch (%q)", ref.CanonicalURL)
}

func (p *sangiinRosterPipeline) autoDiscover(ctx context.Context) {
	// Single fixed roster page; re-discover daily to track membership changes.
	ref, err := p.gw.DiscoverRoster(ctx)
	if err != nil {
		logDiscover(sangiin.RosterSourceName, 0, err)
		return
	}
	if err := p.control.UpsertWatch(ctx, port.Watch{
		StreamID: ref.StreamID, Source: sangiin.RosterSourceName,
		SourceLocalKey: ref.SourceLocalKey, CanonicalURL: ref.CanonicalURL,
	}); err != nil {
		logDiscover(sangiin.RosterSourceName, 0, err)
		return
	}
	logDiscover(sangiin.RosterSourceName, 1, nil)
}

// ── egov-law pipeline ───────────────────────────────────────────────────────

type egovPipeline struct {
	collector *collect.EgovCollector
	projector *project.LawProjector
	differ    *diff.Differ
}

func (e *egovPipeline) source() string { return egov.SourceName }

func (e *egovPipeline) poll(ctx context.Context) int {
	emitted, err := e.collector.PollOnce(ctx, egov.SourceName, pollBatch)
	if err != nil {
		log.Printf("poll: %v", err)
	}
	if emitted > 0 {
		log.Printf("poll: emitted %d events", emitted)
	}
	return emitted
}

func (e *egovPipeline) project(ctx context.Context) {
	projected, err := e.projector.Run(ctx)
	if err != nil {
		log.Printf("project: %v", err)
		return
	}
	if projected > 0 {
		log.Printf("project: %d laws", projected)
	}
	changes, err := e.differ.Run(ctx)
	if err != nil {
		log.Printf("diff: %v", err)
		return
	}
	if changes > 0 {
		log.Printf("diff: %d changes", changes)
	}
}

func (e *egovPipeline) reproject(ctx context.Context) error {
	n, err := e.projector.Reproject(ctx)
	if err != nil {
		return fmt.Errorf("project: %w", err)
	}
	log.Printf("reproject: projected %d laws", n)
	c, err := e.differ.Reproject(ctx)
	if err != nil {
		return fmt.Errorf("diff: %w", err)
	}
	log.Printf("reproject: recomputed %d changes", c)
	return nil
}

func (e *egovPipeline) discover(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("discover", flag.ExitOnError)
	from := fs.String("from", "", "start date YYYY-MM-DD (inclusive; required for --updated)")
	until := fs.String("until", "", "end date YYYY-MM-DD (inclusive; required for --updated)")
	max := fs.Int("max", 0, "cap on discovered resources (0 = no cap)")
	lawType := fs.String("law-type", "", "filter backfill by law_type (Act | CabinetOrder | ...)")
	updated := fs.Bool("updated", false, "discover via 更新法令一覧 over the date window instead of full backfill")
	_ = fs.Parse(args)

	scope := port.ListScope{From: *from, Until: *until, Max: *max}
	var n int
	var err error
	if *updated {
		if *from == "" || *until == "" {
			log.Fatal("discover --updated requires --from and --until (YYYY-MM-DD)")
		}
		n, err = e.collector.DiscoverUpdated(ctx, scope)
	} else {
		n, err = e.collector.Discover(ctx, scope, *lawType)
	}
	if err != nil {
		log.Fatalf("discover: %v", err)
	}
	log.Printf("discover: upserted %d watches", n)
}

func (e *egovPipeline) autoDiscover(ctx context.Context) {
	// 更新法令一覧 over the rolling window picks up newly enacted/amended laws
	// (full backfill stays manual). Same path as `discover --updated`.
	n, err := e.collector.DiscoverUpdated(ctx, recentScope(time.Now()))
	logDiscover(egov.SourceName, n, err)
}

// ── shared driver ───────────────────────────────────────────────────────────

// runDaemon runs the poll side and the project side as two independent loops
// (ADR-000015). Polling is gated by the source's serial rate limit, so a large
// backlog can take a long PollOnce; the project loop runs on its own tick (plus
// an in-process wake when a poll emits events) so it folds events into the read
// models as they are appended, never waiting for the whole poll batch.
func runDaemon(ctx context.Context, p pipeline, source string, control port.ControlStore) {
	log.Printf("collector daemon started (source=%s, poll=%s, project=%s, discover=%s)",
		p.source(), daemonInterval, projectInterval, discoverInterval)

	wake := make(chan struct{}, 1) // live-phase signal: a poll emitted new events

	var wg sync.WaitGroup
	wg.Add(2)

	// Poll loop: refresh the watch list (daily), poll due watches, and wake the
	// project loop when something was appended. Wrapped in recoverLoop so a panic
	// (e.g. a gateway bug on unexpected upstream data) logs and restarts the loop
	// instead of silently killing the whole daemon process (H-C3 sibling: a crash
	// here must not go unnoticed either).
	go func() {
		defer wg.Done()
		recoverLoop(ctx, "poll", func() {
			ticker := time.NewTicker(daemonInterval)
			defer ticker.Stop()
			// Zero value forces an autoDiscover on the first pass so a fresh start (or
			// a restart after downtime) refreshes the watch list before polling
			// (ADR-000012).
			var lastDiscover time.Time
			wasEnabled := true
			for {
				// control.source.enabled is re-read every tick (not just at startup) so
				// an operator's kill switch takes effect on a running daemon, not only
				// on the next restart.
				enabled := true
				if cfg, err := control.Source(ctx, source); err != nil {
					log.Printf("poll: reload source config: %v", err)
				} else {
					enabled = cfg.Enabled
				}
				if !enabled {
					if wasEnabled {
						log.Printf("source %s disabled in control.source; pausing poll loop", source)
					}
					wasEnabled = false
				} else {
					if !wasEnabled {
						log.Printf("source %s re-enabled; resuming poll loop", source)
					}
					wasEnabled = true

					if time.Since(lastDiscover) >= discoverInterval {
						p.autoDiscover(ctx)
						lastDiscover = time.Now()
					}
					if p.poll(ctx) > 0 {
						nudge(wake)
					}
				}

				// Written every tick regardless of enabled/disabled: a heartbeat proves
				// the loop itself is alive, which is exactly what should page an
				// operator even while intentionally paused for long stretches — a
				// wedged loop and a deliberately-disabled source must not look the same.
				if err := control.Heartbeat(ctx, source, time.Now()); err != nil {
					log.Printf("poll: heartbeat: %v", err)
				}

				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		})
	}()

	// Project loop: catch-up subscription. Project once up front, then on each wake
	// (live phase) or tick (catch-up / safety net). The projector is checkpoint-based
	// and idempotent, so running it on its own cadence — concurrently with a long
	// poll — is safe and converges.
	go func() {
		defer wg.Done()
		recoverLoop(ctx, "project", func() {
			ticker := time.NewTicker(projectInterval)
			defer ticker.Stop()
			for {
				p.project(ctx)
				select {
				case <-ctx.Done():
					return
				case <-wake:
				case <-ticker.C:
				}
			}
		})
	}()

	wg.Wait()
	log.Print("collector daemon stopping")
}

// recoverLoop runs body, restarting it after logging if it panics, until ctx is
// done. A panic inside one of the daemon's two loops must not silently kill the
// whole process (leaving `docker restart` as the only recovery signal) — it is
// logged and the loop resumes on the next tick interval instead.
func recoverLoop(ctx context.Context, name string, body func()) {
	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("%s loop panicked, restarting: %v", name, r)
				}
			}()
			body()
		}()
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

// nudge does a non-blocking send so the poll loop never blocks on a busy project
// loop; a coalesced pending wake is enough (the projector folds all new events).
func nudge(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// runCheckpoint signs the current observation log-chain head into a checkpoint
// (ADR-000019). It is idempotent — a no-op when the head is already covered — so it
// is safe to run periodically (cron/timer). `checkpoint genkey [name]` instead prints
// a fresh keypair and never touches the DB.
func runCheckpoint(ctx context.Context, pool *pgxpool.Pool, args []string) {
	if len(args) > 0 && args[0] == "genkey" {
		name := "s4rciv-checkpoint"
		if len(args) > 1 && args[1] != "" {
			name = args[1]
		}
		skey, vkey, err := signer.Generate(name)
		if err != nil {
			log.Fatalf("checkpoint genkey: %v", err)
		}
		// Only the private key goes to stdout, so it can be redirected straight into
		// the secret file (`... genkey > checkpoint.key && chmod 600 checkpoint.key`)
		// without ever crossing a terminal scrollback; everything else goes to stderr.
		fmt.Fprintf(os.Stderr, "# private signing key on stdout — redirect it into the %s secret file (chmod 600), never commit or paste it\n", signer.KeyFileEnv)
		fmt.Println(skey)
		fmt.Fprintf(os.Stderr, "# public verifier key — publish so third parties can verify\n%s\n", vkey)
		return
	}

	s, err := signer.Load()
	if err != nil {
		log.Fatalf("checkpoint: load signing key: %v", err)
	}
	uc := checkpoint.New(postgres.NewCheckpointStore(pool), s, sys.IDGen{})
	created, seq, err := uc.Run(ctx)
	if err != nil {
		log.Fatalf("checkpoint: %v", err)
	}
	if created {
		log.Printf("checkpoint: signed through seq %d", seq)
	} else {
		log.Printf("checkpoint: nothing to do (head seq %d already covered)", seq)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: collector [--source kokkai|egov-law|giin-roster|sangiin-vote|sangiin-roster] <run|poll-once|reproject|discover|checkpoint>

  run         daemon: poll due watches + project on a loop
  poll-once   one poll+project cycle, then exit
  reproject   truncate read models and replay from seq 0
  discover --from YYYY-MM-DD --until YYYY-MM-DD [--max N]
              egov-law also: [--law-type T] [--updated]
  checkpoint  sign the current log-chain head (idempotent; run periodically)
  checkpoint genkey [name]
              print a fresh Ed25519 keypair: the private skey goes to stdout
              (redirect it straight into the secret file, then chmod 600);
              the public vkey to publish goes to stderr
`)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// probeHeartbeat is the container healthcheck entry point (`collector
// --healthcheck [--source S]`): it connects, reads the daemon's last heartbeat
// for source, and exits 0 only if it is fresh. The collector has no HTTP
// listener (unlike cmd/api's /healthz), so this is a short-lived DB round trip
// instead, matching how the db/migrate services already healthcheck via a CLI.
func probeHeartbeat(source string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pool, err := postgres.Connect(ctx)
	if err != nil {
		log.Printf("healthcheck: connect: %v", err)
		return 1
	}
	defer pool.Close()

	beatAt, err := postgres.NewControlStore(pool).LastHeartbeat(ctx, source)
	if err != nil {
		log.Printf("healthcheck: last heartbeat for %s: %v", source, err)
		return 1
	}
	if age := time.Since(beatAt); age > heartbeatStaleAfter {
		log.Printf("healthcheck: stale heartbeat for %s (age %s)", source, age)
		return 1
	}
	return 0
}
