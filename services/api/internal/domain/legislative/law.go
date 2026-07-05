package legislative

// e-Gov 法令 (egov-law) interpretation-plane entities and the 法令標準XML parser.
// Like the kokkai entities, every value is derived deterministically from an
// observation snapshot so projection stays reproject-safe.
//
// The eId derivation here is a SHARED CONTRACT with the Rust differ: the same
// element id must be produced byte-for-byte on both sides so a reported change
// (interpretation.change node refs) joins to its node row. See ADR-000005 and the
// migration comment on interpretation.law_node.

import (
	"encoding/xml"
	"strconv"
	"strings"
	"time"
)

// Node types stored in interpretation.law_node.node_type.
const (
	NodeArticle   = "article"
	NodeParagraph = "paragraph"
	NodeItem      = "item"
	NodeSubitem   = "subitem" // 号の細分 (Subitem イ・ロ・(1)(2)…)
)

// sentenceJoin is the canonical separator inserted between adjacent <Sentence>
// elements owned by one node (e.g. 用語／意義 across two <Column>s, or 本文／ただし書
// across two <Sentence>s). It is a SHARED CONTRACT with the Rust differ (ADR-000013):
// both sides must produce byte-identical sentence_text or every multi-sentence node
// diffs as a permanent MODIFIED.
const sentenceJoin = "　" // 全角スペース (U+3000)

// Law is one 法令 (= one observation stream, keyed by the stable e-Gov 法令ID).
// It observes the law's 現行 (in-force consolidated) 法令標準XML.
type Law struct {
	LawID                    string
	StreamID                 string
	LawNum                   string // 法令番号
	LawType                  string // e-Gov enum: Act / CabinetOrder / ...
	Title                    string
	TitleKana                string
	Category                 string
	PromulgationDate         string // YYYY-MM-DD
	CurrentRevisionID        string // observed law_revision_id ({law_id}_{施行日}_{改正法令ID})
	AmendmentEnforcementDate string // YYYY-MM-DD, 当該版の施行日
	CurrentRevisionStatus    string
	RepealStatus             string
	RepealDate               string // YYYY-MM-DD
	Permalink                string // e-Gov reference URL (attribution)
}

// LawNode is one normative node (条/項/号) in the current tree.
type LawNode struct {
	EID          string
	ParentEID    string
	NodeType     string // article | paragraph | item
	Num          string // 条/項/号 number token ('9', '9_2' for 第9条の2)
	Caption      string // 見出し (ArticleCaption); articles only
	ChapterNum   string // enclosing Chapter Num (container metadata)
	SectionNum   string // enclosing Section Num (container metadata)
	IsSuppl      bool   // under a SupplProvision (附則) vs MainProvision (本則)
	SentenceText string // concatenated normative <Sentence> text owned by the node
	Ordinal      int    // 0-based document-order counter over all materialized nodes
}

// LawContent is the full normalized parse of one law snapshot.
type LawContent struct {
	Law               Law
	Nodes             []LawNode
	SourcePublishedAt *time.Time
}

// LawStreamID is the deterministic stream identity for an e-Gov 法令ID.
func LawStreamID(lawID string) string { return "egov-law:" + lawID }

// LawRevisionStreamID is the deterministic stream identity for one historical
// revision recovered via /law_revisions + /law_data/{revision_id} gap-recovery
// (RecoverRevisions). Deliberately a DIFFERENT stream from LawStreamID(lawID):
// the recurring poll's StreamState (current-content dedup) must never be
// disturbed by a recovered past snapshot, so each revision gets its own,
// never-repolled, single-event stream.
func LawRevisionStreamID(lawID, revisionID string) string {
	return "egov-law-rev:" + lawID + ":" + revisionID
}

// ── 法令標準XML decoding shapes ──────────────────────────────────────────────
// Only the structural elements the eId contract materializes are typed; unknown
// elements are ignored by encoding/xml.

type xmlLaw struct {
	XMLName xml.Name `xml:"Law"`
	LawNum  string   `xml:"LawNum"`
	Body    xmlBody  `xml:"LawBody"`
}

type xmlBody struct {
	LawTitle       xmlLawTitle    `xml:"LawTitle"`
	MainProvision  xmlProvision   `xml:"MainProvision"`
	SupplProvision []xmlProvision `xml:"SupplProvision"`
}

type xmlLawTitle struct {
	Kana  string `xml:"Kana,attr"`
	Value string `xml:",chardata"`
}

// xmlProvision is a MainProvision or SupplProvision. Articles may be nested under
// container elements (Part/Chapter/Section/Subsection/Division) or appear directly.
type xmlProvision struct {
	Parts    []xmlContainer `xml:"Part"`
	Chapters []xmlContainer `xml:"Chapter"`
	Articles []xmlArticle   `xml:"Article"`
}

type xmlContainer struct {
	Num         string         `xml:"Num,attr"`
	Chapters    []xmlContainer `xml:"Chapter"`
	Sections    []xmlContainer `xml:"Section"`
	Subsections []xmlContainer `xml:"Subsection"`
	Divisions   []xmlContainer `xml:"Division"`
	Articles    []xmlArticle   `xml:"Article"`
}

type xmlArticle struct {
	Num        string         `xml:"Num,attr"`
	Caption    string         `xml:"ArticleCaption"`
	Title      string         `xml:"ArticleTitle"`
	Paragraphs []xmlParagraph `xml:"Paragraph"`
}

type xmlParagraph struct {
	Num               string         `xml:"Num,attr"`
	ParagraphSentence xmlSentenceSet `xml:"ParagraphSentence"`
	Items             []xmlItem      `xml:"Item"`
}

type xmlItem struct {
	Num          string         `xml:"Num,attr"`
	ItemSentence xmlSentenceSet `xml:"ItemSentence"`
	Subitems     []xmlSubitem   `xml:"Subitem1"` // 号の細分 (nested Subitem2… handled recursively)
}

// xmlSentenceSet models the (Sentence+ | Column+) content of a *Sentence wrapper
// (ParagraphSentence / ItemSentence / Subitem{n}Sentence). 用語定義号 put the term and
// its 意義 in two <Column>s, so the text is recovered only by descending into Column.
type xmlSentenceSet struct {
	Sentences []xmlSentence `xml:"Sentence"`
	Columns   []xmlColumn   `xml:"Column"`
}

type xmlColumn struct {
	Sentences []xmlSentence `xml:"Sentence"`
}

// xmlSentence captures a <Sentence>'s full descendant text in document order so
// inline markup (Ruby/Sub/Sup/…) survives byte-for-byte with the differ's streaming
// extraction, rather than only the direct chardata.
type xmlSentence struct {
	Inner string `xml:",innerxml"`
}

func (s xmlSentence) text() string { return xmlInnerText(s.Inner) }

// joined returns every owned sentence (direct, then column-wrapped) joined by the
// canonical separator. The content model is a choice, so at most one branch is set.
func (s xmlSentenceSet) joined() string {
	parts := make([]string, 0, len(s.Sentences)+len(s.Columns))
	collect := func(ss []xmlSentence) {
		for _, x := range ss {
			if t := strings.TrimSpace(x.text()); t != "" {
				parts = append(parts, t)
			}
		}
	}
	collect(s.Sentences)
	for _, c := range s.Columns {
		collect(c.Sentences)
	}
	return strings.Join(parts, sentenceJoin)
}

// xmlSubitem is a 号の細分 (<Subitem1>/<Subitem2>/…). The element name encodes the
// depth, so it is decoded level-agnostically: a child <Subitem{n}> recurses and a
// child <Subitem{n}Sentence> carries the text. <Subitem{n}Title> (イ/ロ labels) is
// non-normative and dropped, mirroring how <ItemTitle> is handled.
type xmlSubitem struct {
	Level    int
	Num      string
	Sentence xmlSentenceSet
	Children []xmlSubitem
}

func (s *xmlSubitem) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	s.Level, _ = subitemLevel(start.Name.Local)
	for _, a := range start.Attr {
		if a.Name.Local == "Num" {
			s.Num = a.Value
		}
	}
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			switch {
			case strings.HasPrefix(name, "Subitem") && strings.HasSuffix(name, "Sentence"):
				if err := d.DecodeElement(&s.Sentence, &t); err != nil {
					return err
				}
			case isSubitemElem(name):
				var child xmlSubitem
				if err := child.UnmarshalXML(d, t); err != nil {
					return err
				}
				s.Children = append(s.Children, child)
			default:
				if err := d.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return nil
			}
		}
	}
}

// subitemLevel reports the depth of a bare <Subitem{n}> element name (Subitem1 → 1,
// Subitem10 → 10). It returns ok=false for non-bare names (…Sentence / …Title).
func subitemLevel(name string) (int, bool) {
	rest := strings.TrimPrefix(name, "Subitem")
	if rest == name || rest == "" {
		return 0, false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, _ := strconv.Atoi(rest)
	return n, true
}

func isSubitemElem(name string) bool { _, ok := subitemLevel(name); return ok }

// xmlInnerText concatenates an XML fragment's character data in document order,
// dropping tags but keeping their text (so <Ruby>字<Rt>よみ</Rt></Ruby> yields 字よみ).
func xmlInnerText(inner string) string {
	if inner == "" {
		return ""
	}
	dec := xml.NewDecoder(strings.NewReader(inner))
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if cd, ok := tok.(xml.CharData); ok {
			b.Write(cd)
		}
	}
	return b.String()
}

// containerKind tags which Num column an enclosing container fills.
type containerKind int

const (
	containerOther containerKind = iota
	containerChapter
	containerSection
)

// scope carries the enclosing container metadata down the tree during a walk.
type scope struct {
	chapterNum string
	sectionNum string
}

// builder accumulates nodes with a single document-order ordinal counter and the
// set of eIds already emitted, so derived eIds can be kept unique (see uniq).
type builder struct {
	nodes   []LawNode
	ordinal int
	used    map[string]bool
}

// uniq returns base, or base with a deterministic "~N" suffix when base was already
// emitted in this parse. The eId contract requires uniqueness within a Work version
// (it is the cross-version diff key), but a snapshot can derive the same eId twice
// (e.g. a duplicated Num), which would break the law_node UNIQUE(law_id, eid) read
// model and the diff. uniq is order-stable — the same snapshot always yields the same
// eIds — so reproject stays deterministic. Callers thread the returned eId down as the
// children's prefix and ParentEID, so parent/child linkage is preserved.
func (b *builder) uniq(base string) string {
	if b.used == nil {
		b.used = map[string]bool{}
	}
	cand := base
	for i := 2; b.used[cand]; i++ {
		cand = base + "~" + strconv.Itoa(i)
	}
	b.used[cand] = true
	return cand
}

func (b *builder) add(n LawNode) {
	n.Ordinal = b.ordinal
	b.ordinal++
	b.nodes = append(b.nodes, n)
}

// ParseLawXML parses 法令標準XML into the normalized, current-tree-only domain.
// It is pure with respect to the snapshot bytes (reproject-safe) and implements
// the shared eId contract.
func ParseLawXML(content []byte) (LawContent, error) {
	var x xmlLaw
	if err := xml.Unmarshal(content, &x); err != nil {
		return LawContent{}, err
	}

	b := &builder{}
	walkProvision(b, x.Body.MainProvision, "", scope{})
	for i, sp := range x.Body.SupplProvision {
		walkProvision(b, sp, "suppl_"+strconv.Itoa(i+1)+"__", scope{})
	}

	law := Law{
		LawNum:    strings.TrimSpace(x.LawNum),
		Title:     strings.TrimSpace(x.Body.LawTitle.Value),
		TitleKana: strings.TrimSpace(x.Body.LawTitle.Kana),
	}
	return LawContent{Law: law, Nodes: b.nodes}, nil
}

// walkProvision materializes the articles of one provision, prefixing every eId
// with eidPrefix ("" for 本則, "suppl_{k}__" for the k-th 附則).
func walkProvision(b *builder, p xmlProvision, eidPrefix string, sc scope) {
	for _, c := range p.Parts {
		walkContainer(b, c, eidPrefix, sc, containerOther)
	}
	for _, c := range p.Chapters {
		walkContainer(b, c, eidPrefix, sc, containerChapter)
	}
	for _, a := range p.Articles {
		walkArticle(b, a, eidPrefix, sc)
	}
}

// walkContainer descends a 編/章/節/款/目 wrapper, updating the chapter/section
// metadata carried onto the articles inside it.
func walkContainer(b *builder, c xmlContainer, eidPrefix string, sc scope, kind containerKind) {
	switch kind {
	case containerChapter:
		sc.chapterNum = c.Num
	case containerSection:
		sc.sectionNum = c.Num
	}
	for _, cc := range c.Chapters {
		walkContainer(b, cc, eidPrefix, sc, containerChapter)
	}
	for _, cc := range c.Sections {
		walkContainer(b, cc, eidPrefix, sc, containerSection)
	}
	for _, cc := range c.Subsections {
		walkContainer(b, cc, eidPrefix, sc, containerOther)
	}
	for _, cc := range c.Divisions {
		walkContainer(b, cc, eidPrefix, sc, containerOther)
	}
	for _, a := range c.Articles {
		walkArticle(b, a, eidPrefix, sc)
	}
}

func walkArticle(b *builder, a xmlArticle, eidPrefix string, sc scope) {
	isSuppl := eidPrefix != ""
	artEID := b.uniq(eidPrefix + "art_" + a.Num)
	caption := strings.TrimSpace(a.Caption)

	// An article with no paragraphs still materializes, carrying its body text.
	sentence := ""
	if len(a.Paragraphs) == 0 {
		sentence = strings.TrimSpace(a.Title)
	}
	b.add(LawNode{
		EID:          artEID,
		ParentEID:    "",
		NodeType:     NodeArticle,
		Num:          a.Num,
		Caption:      caption,
		ChapterNum:   sc.chapterNum,
		SectionNum:   sc.sectionNum,
		IsSuppl:      isSuppl,
		SentenceText: sentence,
	})

	for i, p := range a.Paragraphs {
		num := p.Num
		if num == "" {
			num = strconv.Itoa(i + 1) // default to the 1-based ordinal
		}
		paraEID := b.uniq(artEID + "__para_" + num)
		b.add(LawNode{
			EID:          paraEID,
			ParentEID:    artEID,
			NodeType:     NodeParagraph,
			Num:          num,
			ChapterNum:   sc.chapterNum,
			SectionNum:   sc.sectionNum,
			IsSuppl:      isSuppl,
			SentenceText: p.ParagraphSentence.joined(),
		})
		for j, it := range p.Items {
			inum := it.Num
			if inum == "" {
				inum = strconv.Itoa(j + 1)
			}
			itemEID := b.uniq(paraEID + "__item_" + inum)
			b.add(LawNode{
				EID:          itemEID,
				ParentEID:    paraEID,
				NodeType:     NodeItem,
				Num:          inum,
				ChapterNum:   sc.chapterNum,
				SectionNum:   sc.sectionNum,
				IsSuppl:      isSuppl,
				SentenceText: it.ItemSentence.joined(),
			})
			for k, su := range it.Subitems {
				walkSubitem(b, su, itemEID, k+1, sc, isSuppl)
			}
		}
	}
}

// walkSubitem materializes a 号の細分 and its descendants in document order. The eId
// is {parent}__subitem{level}_{Num}; Num defaults to the 1-based sibling ordinal when
// the attribute is absent. Depth lives in the eId, so node_type is the flat 'subitem'.
func walkSubitem(b *builder, su xmlSubitem, parentEID string, sibling int, sc scope, isSuppl bool) {
	num := strings.TrimSpace(su.Num)
	if num == "" {
		num = strconv.Itoa(sibling)
	}
	eid := b.uniq(parentEID + "__subitem" + strconv.Itoa(su.Level) + "_" + num)
	b.add(LawNode{
		EID:          eid,
		ParentEID:    parentEID,
		NodeType:     NodeSubitem,
		Num:          num,
		ChapterNum:   sc.chapterNum,
		SectionNum:   sc.sectionNum,
		IsSuppl:      isSuppl,
		SentenceText: su.Sentence.joined(),
	})
	for k, child := range su.Children {
		walkSubitem(b, child, eid, k+1, sc, isSuppl)
	}
}
