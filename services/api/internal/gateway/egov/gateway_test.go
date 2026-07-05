package egov

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"s4rciv.org/api/internal/blob"
	"s4rciv.org/api/internal/port"
)

// fakeGetter serves canned bodies by endpoint prefix — no live API (DISCIPLINE §1).
type fakeGetter struct {
	bodies map[string][]byte // keyed by endpoint prefix (e.g. "law_data", "laws", "updatelawlists")
	status int
	abs    map[string][]byte // keyed by absolute URL
}

func (f fakeGetter) Get(_ context.Context, endpoint string, _ url.Values) ([]byte, int, error) {
	st := f.status
	if st == 0 {
		st = 200
	}
	for prefix, body := range f.bodies {
		if strings.HasPrefix(endpoint, prefix) {
			return body, st, nil
		}
	}
	return nil, 404, nil
}

func (f fakeGetter) GetAbs(_ context.Context, rawURL string) ([]byte, int, error) {
	if body, ok := f.abs[rawURL]; ok {
		return body, 200, nil
	}
	return nil, 404, nil
}

const lawXML = `<?xml version="1.0" encoding="UTF-8"?>
<Law Era="Heisei" Year="15" LawType="Act" Num="57">
  <LawNum>平成十五年法律第五十七号</LawNum>
  <LawBody>
    <LawTitle Kana="てすとほう">テスト法</LawTitle>
    <MainProvision>
      <Article Num="1">
        <ArticleCaption>（目的）</ArticleCaption>
        <ArticleTitle>第一条</ArticleTitle>
        <Paragraph Num="1">
          <ParagraphSentence><Sentence>本文である。</Sentence></ParagraphSentence>
        </Paragraph>
      </Article>
    </MainProvision>
  </LawBody>
</Law>`

func lawDataBody(t *testing.T) []byte {
	t.Helper()
	env := map[string]any{
		"law_info": map[string]any{
			"law_type": "Act", "law_id": "415AC0000000057",
			"law_num": "平成十五年法律第五十七号", "promulgation_date": "2003-05-30",
		},
		"revision_info": map[string]any{
			"law_revision_id": "415AC0000000057_20240401_506AC0000000010",
			"law_title":       "テスト法",
		},
		"law_full_text": base64.StdEncoding.EncodeToString([]byte(lawXML)),
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal law_data: %v", err)
	}
	return b
}

func TestFetchProducesStableSnapshot(t *testing.T) {
	g := New(fakeGetter{bodies: map[string][]byte{"law_data": lawDataBody(t)}})
	w := port.Watch{SourceLocalKey: "415AC0000000057"}

	r1, err := g.Fetch(context.Background(), w)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !r1.Present || r1.Snapshot == nil {
		t.Fatal("expected present snapshot")
	}
	if r1.Snapshot.MediaType != "application/xml" {
		t.Fatalf("media_type = %q", r1.Snapshot.MediaType)
	}
	if r1.Permalink != "https://laws.e-gov.go.jp/law/415AC0000000057" {
		t.Fatalf("permalink = %q", r1.Permalink)
	}
	if r1.SourcePublishedAt == nil || r1.SourcePublishedAt.Format("2006-01-02") != "2024-04-01" {
		t.Fatalf("source_published_at = %v (want 施行日 2024-04-01)", r1.SourcePublishedAt)
	}

	// C14N is stable: a second fetch yields the same content hash.
	r2, _ := g.Fetch(context.Background(), w)
	if r1.Snapshot.ContentHash != r2.Snapshot.ContentHash {
		t.Fatal("content hash is not stable across fetches")
	}

	// Mirrored bytes decompress to the canonical content that was hashed, and parse.
	raw, err := blob.Decompress(r1.Snapshot.Bytes)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if int64(len(raw)) != r1.Snapshot.ByteSize {
		t.Fatalf("byte_size %d != decompressed len %d", r1.Snapshot.ByteSize, len(raw))
	}
	content, err := g.ParseLaw(raw)
	if err != nil {
		t.Fatalf("ParseLaw: %v", err)
	}
	if content.Law.Title != "テスト法" || len(content.Nodes) != 2 {
		t.Fatalf("parsed law = %+v nodes=%d", content.Law, len(content.Nodes))
	}
}

// lawsBody builds a /laws metadata response listing `count` laws (the existence
// oracle: total_count>0 means the law is still in the registry).
func lawsBody(t *testing.T, count int) []byte {
	t.Helper()
	laws := make([]any, count)
	for i := range laws {
		laws[i] = map[string]any{"law_info": map[string]any{"law_id": "x"}}
	}
	b, err := json.Marshal(map[string]any{"total_count": count, "count": count, "laws": laws})
	if err != nil {
		t.Fatalf("marshal laws: %v", err)
	}
	return b
}

// A law_data 404 while /laws confirms the law is gone (total_count 0) is a
// genuine absence → ResourceVanished.
func TestFetchVanishedWhenLawAbsentFromRegistry(t *testing.T) {
	g := New(fakeGetter{bodies: map[string][]byte{"laws": lawsBody(t, 0)}}) // law_data absent -> 404
	r, err := g.Fetch(context.Background(), port.Watch{SourceLocalKey: "x"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Present || r.ContentUnavailable {
		t.Fatalf("absent law must be not-present and not content-unavailable: %+v", r)
	}
}

// A law_data 404 while /laws still lists the law (e-Gov flipped the revision
// pointer before publishing the new XML) is NOT a vanish — it is ContentUnavailable.
func TestFetchContentUnavailableWhenStillInRegistry(t *testing.T) {
	g := New(fakeGetter{bodies: map[string][]byte{"laws": lawsBody(t, 1)}}) // law_data absent -> 404
	r, err := g.Fetch(context.Background(), port.Watch{SourceLocalKey: "x"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Present {
		t.Fatal("content lag must not report Present")
	}
	if !r.ContentUnavailable {
		t.Fatal("law still in registry but no full text must be ContentUnavailable (not a vanish)")
	}
}

// A 200 with an empty law_full_text resolves through the same oracle: still in
// the registry → ContentUnavailable, not a vanish.
func TestFetchEmptyFullTextWithLawPresentIsContentUnavailable(t *testing.T) {
	empty, _ := json.Marshal(map[string]any{
		"law_info":      map[string]any{"law_id": "x"},
		"law_full_text": "",
	})
	g := New(fakeGetter{bodies: map[string][]byte{
		"law_data": empty,
		"laws":     lawsBody(t, 1),
	}})
	r, err := g.Fetch(context.Background(), port.Watch{SourceLocalKey: "x"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Present || !r.ContentUnavailable {
		t.Fatalf("empty full text with law present must be ContentUnavailable: %+v", r)
	}
}

// When existence cannot be confirmed (the /laws lookup itself errors with a
// non-200), Fetch errors rather than fabricating an absence — a vanish requires
// positive confirmation.
func TestFetchErrorsWhenExistenceUnconfirmed(t *testing.T) {
	// law_data is absent (404) and the /laws existence lookup returns a non-200,
	// so existence cannot be confirmed.
	g := New(fakeGetter{bodies: map[string][]byte{"laws": lawsBody(t, 1)}, status: 500})
	_, err := g.Fetch(context.Background(), port.Watch{SourceLocalKey: "x"})
	if err == nil {
		t.Fatal("unconfirmed existence must error, not silently vanish")
	}
}

func TestListLawsPaging(t *testing.T) {
	// A static fakeGetter returns the same body for every "laws" call; the gateway
	// terminates when next_offset is 0 (JSON null/absent on the terminal page), so
	// this single page is listed once and the traversal returns.
	page0, _ := json.Marshal(map[string]any{
		"total_count": 1, "count": 1, "next_offset": 0,
		"laws": []any{map[string]any{"law_info": map[string]any{"law_id": "A1"}}},
	})
	g := New(fakeGetter{bodies: map[string][]byte{"laws": page0}})
	refs, err := g.ListLaws(context.Background(), port.ListScope{}, "Act")
	if err != nil {
		t.Fatalf("ListLaws: %v", err)
	}
	if len(refs) != 1 || refs[0].StreamID != "egov-law:A1" {
		t.Fatalf("refs = %+v", refs)
	}
	if refs[0].CanonicalURL != "https://laws.e-gov.go.jp/law/A1" {
		t.Fatalf("canonical_url = %q", refs[0].CanonicalURL)
	}
}

// pagerGetter computes each /laws page from the offset query param, simulating an
// upstream whose cursor we do not control.
type pagerGetter struct {
	page func(offset int) []byte
}

func (p pagerGetter) Get(_ context.Context, _ string, q url.Values) ([]byte, int, error) {
	offset, _ := strconv.Atoi(q.Get("offset"))
	return p.page(offset), 200, nil
}

func (p pagerGetter) GetAbs(context.Context, string) ([]byte, int, error) {
	return nil, 404, nil
}

func lawsPage(t *testing.T, offset, next int) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"total_count": 1, "count": 1, "next_offset": next,
		"laws": []any{map[string]any{"law_info": map[string]any{"law_id": fmt.Sprintf("L%08d", offset)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An upstream that returns a positive but non-advancing next_offset must abort
// the traversal with an error instead of looping forever on a trusted cursor.
func TestListLawsAbortsOnNonAdvancingCursor(t *testing.T) {
	g := New(pagerGetter{page: func(offset int) []byte {
		return lawsPage(t, offset, 100) // stuck at 100: advances once, then loops
	}})
	_, err := g.ListLaws(context.Background(), port.ListScope{}, "")
	if err == nil || !strings.Contains(err.Error(), "non-advancing") {
		t.Fatalf("non-advancing cursor must abort with an error, got %v", err)
	}
}

// Even an always-advancing cursor is bounded by the page ceiling, so a runaway
// upstream listing cannot drive an unbounded crawl.
func TestListLawsAbortsAfterPageCeiling(t *testing.T) {
	g := New(pagerGetter{page: func(offset int) []byte {
		return lawsPage(t, offset, offset+100) // advances forever
	}})
	_, err := g.ListLaws(context.Background(), port.ListScope{}, "")
	if err == nil || !strings.Contains(err.Error(), "pages") {
		t.Fatalf("runaway pagination must abort at the page ceiling, got %v", err)
	}
}

func TestListUpdatedFiltersByEnforcement(t *testing.T) {
	body, _ := json.Marshal([]map[string]any{
		{"LawId": "A1", "EnforcementFlg": "0", "AuthFlg": "0"},
		{"LawId": "A2", "EnforcementFlg": "1", "AuthFlg": "0"}, // 未施行 -> skipped
		{"LawId": "A3", "EnforcementFlg": "0", "AuthFlg": "1"},
	})
	g := New(fakeGetter{bodies: map[string][]byte{"updatelawlists": body}})
	refs, err := g.ListUpdated(context.Background(), port.ListScope{From: "2024-04-01", Until: "2024-04-01"})
	if err != nil {
		t.Fatalf("ListUpdated: %v", err)
	}
	if len(refs) != 2 {
		t.Fatalf("refs = %d, want 2 (in-force only): %+v", len(refs), refs)
	}
	got := map[string]bool{refs[0].SourceLocalKey: true, refs[1].SourceLocalKey: true}
	if !got["A1"] || !got["A3"] {
		t.Fatalf("expected A1 and A3, got %+v", refs)
	}
}

func TestListRevisions(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"revisions": []map[string]any{
			{
				"law_revision_id":            "415AC0000000057_20240401_506AC0000000010",
				"amendment_promulgate_date":  "2024-03-01",
				"amendment_enforcement_date": "2024-04-01",
				"amendment_law_id":           "506AC0000000010",
			},
			{
				// Blank dates (未施行/repeal-not-applicable revisions) must parse to nil,
				// not error the whole listing.
				"law_revision_id": "415AC0000000057_20180401_500AC0000000001",
			},
		},
	})
	g := New(fakeGetter{bodies: map[string][]byte{"law_revisions": body}})
	revs, err := g.ListRevisions(context.Background(), "415AC0000000057")
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("revisions = %d, want 2", len(revs))
	}
	if revs[0].RevisionID != "415AC0000000057_20240401_506AC0000000010" {
		t.Fatalf("revision_id = %q", revs[0].RevisionID)
	}
	if revs[0].EnforcementDate == nil || revs[0].EnforcementDate.Format("2006-01-02") != "2024-04-01" {
		t.Fatalf("enforcement_date = %v", revs[0].EnforcementDate)
	}
	if revs[0].PromulgateDate == nil || revs[0].PromulgateDate.Format("2006-01-02") != "2024-03-01" {
		t.Fatalf("promulgate_date = %v", revs[0].PromulgateDate)
	}
	if revs[0].AmendmentLawID != "506AC0000000010" {
		t.Fatalf("amendment_law_id = %q", revs[0].AmendmentLawID)
	}
	if revs[1].EnforcementDate != nil || revs[1].PromulgateDate != nil {
		t.Fatalf("blank dates must parse to nil: %+v", revs[1])
	}
}

func TestFetchRevisionProducesSnapshot(t *testing.T) {
	g := New(fakeGetter{bodies: map[string][]byte{"law_data": lawDataBody(t)}})
	r, err := g.FetchRevision(context.Background(), "415AC0000000057_20240401_506AC0000000010")
	if err != nil {
		t.Fatalf("FetchRevision: %v", err)
	}
	if !r.Present || r.Snapshot == nil {
		t.Fatal("expected present snapshot")
	}
	// Permalink is derived from the law_id prefix of the revision_id, not the
	// revision_id itself — a revision has no separate permalink.
	if r.Permalink != "https://laws.e-gov.go.jp/law/415AC0000000057" {
		t.Fatalf("permalink = %q, want derived from law_id prefix", r.Permalink)
	}
}

// A missing historical revision (404, or a 200 with empty law_full_text) is a
// reportable gap — that specific snapshot is absent from e-Gov — and must NOT
// be resolved through the law-existence oracle (/laws): unlike Fetch, a 404 here
// says nothing about whether the LAW itself still exists, so it must never
// surface as ContentUnavailable (which claims "law confirmed to still exist")
// or route through absenceOrPending at all.
func TestFetchRevision_404NotTreatedAsVanish(t *testing.T) {
	g := New(fakeGetter{}) // no bodies registered -> law_data/{revisionID} 404s
	r, err := g.FetchRevision(context.Background(), "415AC0000000057_19990401_000AC0000000001")
	if err != nil {
		t.Fatalf("FetchRevision: %v", err)
	}
	if r.Present {
		t.Fatal("missing revision must not report Present")
	}
	if r.ContentUnavailable {
		t.Fatal("missing revision is a gap, not ContentUnavailable (that oracle is Fetch-only)")
	}
}

// Differ-compatibility regression (services/differ requires no code change for
// v2): this drives the exact v2 response shape (law_full_text_format=xml +
// response_format=json => base64 XML) through Fetch -> canonicalizeXML and
// checks the canonical output preserves the Article/Paragraph/Sentence
// structure that services/differ/tests/diff_contract.rs's base_law() fixture
// (and xmlmodel::parse's eId derivation, ADR-000005/000013) depends on. The
// Rust differ only ever sees these bytes over the transport-agnostic
// s4rciv.diff.v1.DiffService contract, so proving this Go-side shape is stable
// is sufficient — no Rust-side test is needed for the v1->v2 migration itself.
func TestFetchCanonicalXMLPreservesDifferContractStructure(t *testing.T) {
	g := New(fakeGetter{bodies: map[string][]byte{"law_data": lawDataBody(t)}})
	r, err := g.Fetch(context.Background(), port.Watch{SourceLocalKey: "415AC0000000057"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	raw, err := blob.Decompress(r.Snapshot.Bytes)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	canonical := string(raw)
	for _, want := range []string{
		`Num="1"`, "<ArticleCaption>", "<ArticleTitle>", "<Paragraph", "<Sentence>",
	} {
		if !strings.Contains(canonical, want) {
			t.Fatalf("canonical XML missing %q (differ contract structure not preserved):\n%s", want, canonical)
		}
	}
}

func TestListUpdatedV1Fallback(t *testing.T) {
	// v2 has no updatelawlists endpoint (404); the v1 fallback serves text/xml:
	// DataRoot > ApplData > LawNameListInfo[]. This is the live path in production.
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<DataRoot>
  <Result><Code>0</Code><Message/></Result>
  <ApplData>
    <Date>20240401</Date>
    <LawNameListInfo><LawId>B9</LawId><EnforcementFlg>0</EnforcementFlg><AuthFlg>0</AuthFlg></LawNameListInfo>
    <LawNameListInfo><LawId>B8</LawId><EnforcementFlg>1</EnforcementFlg></LawNameListInfo>
  </ApplData>
</DataRoot>`)
	g := New(fakeGetter{
		bodies: map[string][]byte{},
		abs: map[string][]byte{
			updateListV1Base + "/updatelawlists/20240401": body,
		},
	})
	refs, err := g.ListUpdated(context.Background(), port.ListScope{From: "2024-04-01", Until: "2024-04-01"})
	if err != nil {
		t.Fatalf("ListUpdated: %v", err)
	}
	// B8 is 未施行 (EnforcementFlg=1) and must be filtered out, leaving B9.
	if len(refs) != 1 || refs[0].SourceLocalKey != "B9" {
		t.Fatalf("v1 XML fallback refs = %+v", refs)
	}
}
