# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Key documents — read these first

| File | Role | Authority |
|---|---|---|
| [`docs/concepts/CORE_CONCEPT_0001.md`](docs/concepts/CORE_CONCEPT_0001.md) | Single source of truth: purpose, scope, architecture, event schema, abuse model (Japanese) | **Wins on any conflict** |
| [`DISCIPLINE.md`](DISCIPLINE.md) | The canonical list of **what you must NOT do** (rate limits, passivity, anti-doxxing, data integrity, …) | Binding. Read before writing any collector or touching the data layer |
| `CLAUDE.md` (this file) | Quick orientation and summary | Defers to the two above |

## Project state

**M1 (国会会議録 adapter) implemented; v0 scaffold otherwise.** Docker Compose stack (`compose.yaml`, project `s4rciv`): Postgres 18 (`db`), Atlas migrations (`migrate`), a Go `api` (read-only Connect-RPC query side), a Go `collector` (M1 command side; same `services/api` module, separate build target), a Rust differ stub (`services/differ`), and a SvelteKit web stub (`web`). The **immutable data model schema is designed and migrated** — three schemas: `observation` (immutable, append-only, hash-chained ground truth), `interpretation` (two-tier: append-only `interpretation.event` + disposable read models, incl. the M1 kokkai read models), `control` (mutable operational state). See `db/migrations/`, `CONTEXT.md` (glossary), and `docs/ADR/000001`–`000004`. Local stack ops live in the `docker-compose` skill.

### M1 layout (`services/api`, clean architecture)

`cmd/api` (Connect-RPC query Handler) · `cmd/collector` (CLI Handler: `run`/`poll-once`/`reproject`/`discover`) → `internal/usecase/{collect,project}` → `internal/port` → `internal/gateway/kokkai` (anti-corruption + JCS canonicalization) + `internal/driver/{postgres,kokkaihttp,sys}`. Pure core in `internal/domain/{observation,legislative}` (log-hash via `HashableEvent` proto, ADR-000003; vote parser; Popolo identity). Protobuf is `proto/` → committed `gen/` via buf.

### Commands (run from `services/api`)

- `go test ./...` — unit tests (domain/usecase/gateway/handler, fakes/fixtures, no live API). The `postgres` drivers now have a real-DB integration suite (next section), not just build-verification.
- `go vet ./...` — vet.
- `buf generate` (+ `buf lint`) — regenerate `gen/` after editing `proto/` (needs `protoc-gen-go` + `protoc-gen-connect-go` on PATH).
- `atlas migrate hash --env local` (from repo root) — rehash after adding a migration.
- `collector run|poll-once|reproject|discover --from YYYY-MM-DD --until YYYY-MM-DD` — collector subcommands.

### Test suite (run from repo root; ADR-000016)

The host toolchain (Go 1.27, protoc, Atlas) is **not** relied on — every DB-touching layer runs in containers against the real Postgres from `compose.yaml`, so `git clone && make test` reproduces the whole suite with only Docker (+ Node for browser E2E). `make help` lists targets.

- `make unit` — Go + Rust + web (Vitest) unit tests, hermetic.
- `make cdc` — contract checks: `scripts/check-proto-drift.sh` (api vs differ `diff.proto` byte-equality) + `buf breaking` vs the base ref. No Pact (shared buf-generated `.proto` makes it redundant).
- `make integration` — Go `postgres` drivers vs a real migrated Postgres, `//go:build integration`, **template-DB-per-test** (`CREATE DATABASE … TEMPLATE`), `-race`. Asserts the append-only / log-chain / trigger / CHECK invariants a fake can't.
- `make e2e` — Playwright browser journeys vs the real stack, deterministically seeded by `internal/e2eseed` (reuses `EventFacts.LogHash`; fictional names only). Needs `pnpm exec playwright install chromium` once.
- `make test` — all of the above, then teardown; `make down` to tear down manually.
- web: `pnpm test` (Vitest: `verification` CDC project + `component` project). The in-browser verifier CDC (ADR-000014) lives in `web/src/lib/verification/`.

## What S4RCIV is

A **passive, read-only "flight recorder" for public records** plus a situation-room dashboard for citizens. It continuously collects Japanese public primary-source data (legislation, laws/ordinances, public money, procurement) and records *changes* — including deletions — into an immutable, hash-chained log, so anyone can trace **when / what / how something changed or was removed**. Non-partisan civic-tech infrastructure in the g0v / Audrey Tang lineage; a modern successor to EDGI Web Monitoring.

## Design principles (the reason the project exists)

These are constraints, not aspirations. Any feature or dependency that violates one is wrong by definition — surface the conflict instead of building it. The **operational prohibitions derived from these are enumerated in [`DISCIPLINE.md`](DISCIPLINE.md)**; the summary here is for orientation.

1. **Passive / read-only** — public-endpoint HTTP GET only. No auth, no submissions, no writes, no automated actions. A *sentinel*, never an actor.
2. **Public primary sources only.**
3. **Append-only, hash-chained log** — keep everything (incl. deletions/reversions); the log is tamper-evident so S4RCIV can prove it has not rewritten history.
4. **Dual-plane separation** — *observation plane* (raw snapshots + hash-chained events; immutable ground truth) vs *interpretation plane* (normalized entities + classification + LLM summaries; recomputable projections carrying provenance + confidence).
5. **Standards-based, no silos** — Akoma Ntoso (laws/proceedings), Popolo (people/roles), OCDS (procurement).
6. **AI summarizes only, never judges** — no scoring/opinions in the data layer; every summary links back to source text/diff with confidence + provenance.
7. **Built-in source compliance** — per-source rate limiting, robots.txt, identifying User-Agent, attribution + fetch timestamp on every record, Internet Archive dual-sourcing where possible.

The hardest, most load-bearing rules: **never profile/dox private individuals** (the target is always accountable public actors), **never act partisanly** (same mechanical pipeline for all), and **never present a decontextualized diff** (always with surrounding context + full-text link).

## Planned architecture (target, not yet built)

Single self-hosted binary, adapter-based, embedded event store.

```
Source Adapters (Rust/Go, HTTP GET only)
  → Normalizer (AKN / Popolo / OCDS + diff/classify)
  → Event Log (CQRS, append-only, hash-chained = observation plane)
  → Read Models (timeline / entity / vote / contract / funding + LLM summaries = interpretation plane)
  → Web (SvelteKit + MapLibre/WebGPU situation-room dashboard)
```

- **Source adapters are the unit of extension.** New source = new pluggable adapter (collect + normalize), loosely coupled and versioned. Record observation gaps as `ResourceVanished` (silence is information).
- **Observation events**: `ResourceObserved`, `ResourceChanged`, `ResourceVanished`, `ResourceRestored`. Envelope schema (uuidv7 event_id, stream_id, seq, content_hash, prev_content_hash, log_prev_hash, payload_ref, diff, confidence) in concept §9.1.
- **Diff precision by source type**: structured XML/AKN → structural diff (article/clause level); semi-structured/PDF → text diff with `confidence=low` + source-PDF link. Changes heuristically classified `administrative` vs `substantive`; `substantive` → human review queue, and the review result is itself an interpretation-plane event.

### MVP order (concept §11)

M0 skeleton (binary, hash-chained event log, adapter interface, plane separation) → M1 国会会議録 API adapter (Diet proceedings, VoteEvents) → M2 e-Gov 法令 API v2 adapter (laws, AKN structural diff) → M3 dashboard v0 → M4 map → M5 summaries v0 → M6 public release.

## Source catalog (priority order; concept §6)

MVP sources:

- **国会会議録検索API** (`https://kokkai.ndl.go.jp/api/`) — three GET endpoints: `meeting_list` (metadata, ≤100/req), `meeting` (full speech text, ≤10/req), `speech` (per-speech, ≤100/req). JSON via `recordPacking=json`; pagination via `startRecord` / `maximumRecords` / response `nextRecordPosition`; search-condition total ≤2000 bytes; stream unit = 21-char `issueID`. No registration, but **serial access with a few seconds between requests** is required and abuse is blocked without notice. NDL holds copyright on the database and on NDL-staff speeches → attribution mandatory. Coverage: 1st Diet (1947)–present. Spec: `https://kokkai.ndl.go.jp/api.html`
- **e-Gov 法令API v2** (`https://laws.e-gov.go.jp/api/2/`) — change detection via 更新法令一覧 (`https://laws.e-gov.go.jp/update/`).

Later: 官報/告示 base registry (≈2026), 政治資金 (online publication mandated 2027-01), procurement/OCDS (`https://www.p-portal.go.jp/`).

## Open questions affecting implementation (concept §14)

Deliberately unresolved — confirm with the user before baking in: hosting model; self-mirroring vs Internet-Archive-linking of primary documents; administrative/substantive classification heuristics and thresholds; low-confidence OCR handling in the UI; private-donor display granularity; alert delivery boundaries.

## Available skills (Claude Code)

Project skills live in `.claude/skills/` and are auto-discovered (no registration). Transferred from the sibling Alt project and adapted to S4RCIV. Reference docs for the `bp-*` skills live in `docs/best_practices/`.

| Skill | Purpose | Fires on |
|---|---|---|
| `bp-rust` / `bp-go` / `bp-python` / `bp-svelte` / `bp-typescript` | Language best practices (DECREE) | editing `.rs` / `.go` / `.py` / `.svelte` / `.ts` files |
| `clean-architecture` | Handler→Usecase→Port→Gateway→Driver layers, mapped to the adapter model + plane separation | layered backend work |
| `immutable-design-guard` | Audits append-only event log, hash-chain integrity, reproject-safe projectors, disposable read models | migrations / projectors / event handlers; "イミュータブル", "event sourcing", "reproject" |
| `security-auditor` | OWASP Top 10:2025 / ASVS 5.0 security review + S4RCIV collector compliance (SSRF / rate-limit / robots.txt / read-only) | "セキュリティレビュー", "脆弱性チェック", "OWASP" |
| `web-researcher` | Official-docs-first web research → structured report | "調べて", "リサーチして", "公式ドキュメント確認して" |
| `tdd-workflow` | Outside-in TDD (E2E → CDC → unit RED-GREEN-REFACTOR) + local CI parity | "TDDで", feature/bugfix work |
| `s4rciv-adr-writer` | ADR authoring in Japanese (no deploy); template at `docs/ADR/template.md` | "ADR書いて", "ADRにまとめて", "ADRに記録して" |
| `docker-compose` | Local stack command reference (db / migrate(Atlas) / api(Go) / differ(Rust) / web(SvelteKit)) | running / inspecting containers, applying migrations |

`postmortem-writer` is available globally (user scope) — fires on "ポストモーテム書いて" / "incident report" / "RCA". `log-seeker` and `plan-context-loader` were intentionally **not** transferred (they need running infra / an ADR corpus that does not exist yet).

Python type checking uses **Pyrefly**, not mypy.

## License

The server body (this repository) is **AGPL-3.0**, per concept §13 and mySociety-style civic-tech convention (keep SaaS forks open). Decided separately when those artifacts exist: data/schema license (CC0 or CC BY) and client-library license (Apache-2.0/MIT) — not covered by the repository `LICENSE`.
