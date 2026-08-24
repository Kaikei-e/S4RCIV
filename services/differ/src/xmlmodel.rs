//! Parse 法令標準XML (e-Gov law standard XML) into a flat map of normative nodes
//! keyed by AKN eId.
//!
//! eId derivation is a SHARED CONTRACT with the Go projector (ADR-000005): the
//! differ's output nodes must join byte-for-byte to the Go-side `law_node` rows.
//! The rules implemented here are:
//!
//! - Article   → `art_{Num}`                       (Num verbatim, `_` for 枝番 e.g. `9_2`)
//! - Paragraph → `{article_eid}__para_{Num}`        (Num defaults to 1-based ordinal)
//! - Item      → `{paragraph_eid}__item_{Num}`
//! - Subitem   → `{parent_eid}__subitem{level}_{Num}` (号の細分 イ/ロ/(1)(2)…; the
//!   `<Subitem{level}>` depth flows into the eId, e.g. `…__item_2__subitem1_2__subitem2_1`)
//! - 附則      → every node under the k-th `<SupplProvision>` (1-based among
//!   siblings) gets the prefix `suppl_{k}__`, e.g. `suppl_1__art_3__para_1`.
//!
//! `node_type` is one of `article` | `paragraph` | `item` | `subitem`. `sentence_text`
//! is the node's directly-owned text, joining adjacent `<Sentence>`s with the canonical
//! 全角スペース (U+3000) and descending into `<Column>` (用語定義号 split the term and
//! its 意義 across two `<Column>`s). This text-join + the Subitem eId scheme are a SHARED
//! CONTRACT with the Go projector (ADR-000013) and must stay byte-identical.
//! - paragraph: the sentences inside its own `<ParagraphSentence>`,
//! - item: the sentences inside its own `<ItemSentence>`,
//! - subitem: the sentences inside its own `<Subitem{level}Sentence>`,
//! - article with no paragraphs: its body text excluding `<ArticleCaption>`.
//!
//! `Num` is an XML attribute on `<Article>` / `<Paragraph>` / `<Item>` / `<Subitem{n}>`
//! in the law standard; we read it off the start tag. 枝番 such as 第9条の2 appear as
//! `Num="9_2"` and flow into the eId verbatim.

use std::collections::BTreeMap;

use quick_xml::Reader;
use quick_xml::events::{BytesStart, Event};

/// A normative node extracted from the law XML, keyed externally by its eId.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Node {
    pub eid: String,
    pub node_type: NodeType,
    /// The `Num` token as it appears in the source (or a synthesized ordinal).
    pub num: String,
    /// The joined normative `<Sentence>` text directly owned by this node.
    pub sentence_text: String,
    /// Caption text (`<ArticleCaption>`), tracked so caption-only changes can be
    /// classified as administrative. Empty for non-article nodes.
    pub caption: String,
    /// Parent eId, used to detect MOVED. Empty for top-level articles.
    pub parent_eid: String,
    /// Document order, used for stable output ordering.
    pub ordinal: usize,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NodeType {
    Article,
    Paragraph,
    Item,
    Subitem,
}

impl NodeType {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Article => "article",
            Self::Paragraph => "paragraph",
            Self::Item => "item",
            Self::Subitem => "subitem",
        }
    }
}

/// The canonical separator between adjacent `<Sentence>`s owned by one node — the
/// shared sentence-join contract with the Go projector (ADR-000013). Must match
/// `legislative.sentenceJoin` byte-for-byte.
const SENTENCE_JOIN: char = '　'; // 全角スペース (U+3000)

/// Maximum nesting depth of 号の細分 (`<Subitem{n}>`). Real 法令標準XML defines
/// Subitem1..Subitem10, so 16 is generous; deeper nesting flags the parse
/// degraded and the extra levels are skipped (CWE-400: each level repeats the
/// full ancestor eId prefix, so unbounded depth amplifies memory quadratically).
const MAX_SUBITEM_DEPTH: usize = 16;

/// Maximum length (in chars) of a `Num` attribute flowing into an eId. Real Num
/// tokens are a few characters (e.g. `9_2`); longer values are truncated and the
/// parse flagged degraded, since an oversized Num would be copied into every
/// descendant eId.
const MAX_NUM_LEN: usize = 256;

/// Maximum total node count per document. Beyond this the parse is flagged
/// degraded and further nodes are dropped, bounding the node map on
/// adversarial input.
const MAX_NODE_COUNT: usize = 1_000_000;

/// The result of parsing one snapshot.
#[derive(Debug, Default)]
pub struct ParsedLaw {
    /// eId → node, ordered by eId for deterministic iteration.
    pub nodes: BTreeMap<String, Node>,
    /// True when parsing hit a structural problem and the node map is degraded.
    /// Drives `class_confidence = low`.
    pub degraded: bool,
}

#[derive(Debug)]
pub enum ParseError {
    Xml(quick_xml::Error),
}

impl std::fmt::Display for ParseError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Xml(e) => write!(f, "xml parse error: {e}"),
        }
    }
}

impl std::error::Error for ParseError {}

impl From<quick_xml::Error> for ParseError {
    fn from(e: quick_xml::Error) -> Self {
        Self::Xml(e)
    }
}

/// Parse a 法令標準XML byte slice into an eId-keyed node map.
///
/// An empty slice yields an empty `ParsedLaw` (used for the first-revision case
/// where `prev_snapshot` is `[]`).
pub fn parse(xml: &[u8]) -> Result<ParsedLaw, ParseError> {
    if xml.is_empty() {
        return Ok(ParsedLaw::default());
    }

    let mut reader = Reader::from_reader(xml);
    let cfg = reader.config_mut();
    cfg.trim_text(true);

    let mut builder = Builder::default();
    let mut buf = Vec::new();

    loop {
        match reader.read_event_into(&mut buf) {
            Ok(Event::Eof) => break,
            Ok(Event::Start(e)) => {
                let name = local_name(e.name().as_ref());
                let num = num_attr(&e);
                builder.on_start(&name, num);
            }
            Ok(Event::Empty(e)) => {
                // Self-closing tags carry no text; treat as start+end so Num-bearing
                // empty leaves still register (rare, but keeps the model consistent).
                let name = local_name(e.name().as_ref());
                let num = num_attr(&e);
                builder.on_start(&name, num);
                builder.on_end(&name);
            }
            Ok(Event::End(e)) => {
                let name = local_name(e.name().as_ref());
                builder.on_end(&name);
            }
            Ok(Event::Text(e)) => {
                // quick-xml emits entity references as separate GeneralRef events,
                // so Text content here is already plain text. Since 0.42 events
                // carry `str`, and the reader has already validated UTF-8.
                let text: &str = &e;
                builder.on_text(text);
            }
            Ok(Event::GeneralRef(r)) => {
                // Resolve the five predefined XML entities; anything else is passed
                // through verbatim so we never drop normative characters.
                let raw: &str = &r;
                let resolved = match raw {
                    "amp" => "&",
                    "lt" => "<",
                    "gt" => ">",
                    "quot" => "\"",
                    "apos" => "'",
                    other => {
                        // Numeric character references (&#...;) carry a leading '#'.
                        if let Some(decoded) = decode_char_ref(other) {
                            builder.on_text(&decoded);
                            buf.clear();
                            continue;
                        }
                        other
                    }
                };
                builder.on_text(resolved);
            }
            Ok(_) => {}
            Err(e) => return Err(ParseError::Xml(e)),
        }
        buf.clear();
    }

    Ok(builder.finish())
}

/// Strip an XML namespace prefix, returning the local element name.
fn local_name(raw: &str) -> String {
    match raw.rsplit_once(':') {
        Some((_, local)) => local.to_string(),
        None => raw.to_string(),
    }
}

/// Decode a numeric character reference body (`#123` or `#x1F`) to its character.
fn decode_char_ref(body: &str) -> Option<String> {
    let rest = body.strip_prefix('#')?;
    let code = if let Some(hex) = rest.strip_prefix(['x', 'X']) {
        u32::from_str_radix(hex, 16).ok()?
    } else {
        rest.parse::<u32>().ok()?
    };
    char::from_u32(code).map(|c| c.to_string())
}

/// Read the `Num` attribute off a start tag, if present.
fn num_attr(e: &BytesStart<'_>) -> Option<String> {
    for attr in e.attributes().flatten() {
        if attr.key.as_ref() == "Num" {
            return Some(attr.value.into_owned());
        }
    }
    None
}

/// Normalize a `Num` token for eId use. The law standard already encodes 枝番 as
/// `9_2`; we keep it verbatim and only trim surrounding whitespace.
fn normalize_num(num: &str) -> String {
    num.trim().to_string()
}

/// Where the current text run should be appended.
#[derive(Clone, Copy, PartialEq, Eq, Default)]
enum TextSink {
    #[default]
    None,
    /// Inside `<ArticleTitle>` / other non-normative label text — discard.
    Discard,
    ArticleCaption,
    ParagraphSentence,
    ItemSentence,
    SubitemSentence,
}

/// Streaming state machine that turns SAX-style events into the node map.
///
/// We track the current 附則 prefix, the open Article/Paragraph/Item context, and
/// route `<Sentence>` text into the correct owner.
#[derive(Default)]
struct Builder {
    law: ParsedLaw,
    ordinal: usize,

    /// 1-based index of the current `<SupplProvision>`, 0 when in MainProvision.
    suppl_index: usize,
    in_suppl: bool,

    cur_article: Option<OpenArticle>,
    cur_paragraph: Option<OpenParagraph>,
    cur_item: Option<OpenItem>,
    /// Open 号の細分 stack (innermost last), so <Subitem2> nests under <Subitem1>.
    cur_subitems: Vec<OpenSubitem>,
    /// Number of currently open `<Subitem{n}>`s skipped for exceeding
    /// `MAX_SUBITEM_DEPTH`; their end tags must not pop the real stack.
    skipped_subitem_depth: usize,

    /// Whether the current article has emitted at least one paragraph; if not,
    /// the article owns body text directly.
    article_has_paragraph: bool,

    sink: TextSink,
}

struct OpenArticle {
    eid: String,
    num: String,
    caption: String,
    /// Body text for paragraph-less articles (sentences directly under Article).
    body: String,
    ordinal: usize,
    para_ordinal: usize,
}

struct OpenParagraph {
    eid: String,
    num: String,
    text: String,
    ordinal: usize,
    item_ordinal: usize,
}

struct OpenItem {
    eid: String,
    num: String,
    text: String,
    ordinal: usize,
    subitem_ordinal: usize,
}

struct OpenSubitem {
    eid: String,
    num: String,
    text: String,
    ordinal: usize,
    child_ordinal: usize,
}

impl Builder {
    fn next_ordinal(&mut self) -> usize {
        self.ordinal += 1;
        self.ordinal
    }

    fn prefix(&self) -> String {
        if self.in_suppl {
            format!("suppl_{}__", self.suppl_index)
        } else {
            String::new()
        }
    }

    /// Truncate an oversized `Num` to `MAX_NUM_LEN` chars, flagging the parse
    /// degraded. Capping here (before any eId is built) keeps every eId bounded.
    fn cap_num(&mut self, num: String) -> String {
        if num.chars().count() <= MAX_NUM_LEN {
            return num;
        }
        self.law.degraded = true;
        num.chars().take(MAX_NUM_LEN).collect()
    }

    fn on_start(&mut self, name: &str, num: Option<String>) {
        let num = num.map(|n| self.cap_num(n));
        match name {
            "SupplProvision" => {
                self.in_suppl = true;
                self.suppl_index += 1;
            }
            "Article" => {
                let num = num.unwrap_or_default();
                let eid = format!("{}art_{}", self.prefix(), normalize_num(&num));
                let ordinal = self.next_ordinal();
                self.cur_article = Some(OpenArticle {
                    eid,
                    num,
                    caption: String::new(),
                    body: String::new(),
                    ordinal,
                    para_ordinal: 0,
                });
                self.article_has_paragraph = false;
            }
            "ArticleCaption" => self.sink = TextSink::ArticleCaption,
            // Label/title text is non-normative; route it to the discard sink so it
            // never leaks into a paragraph-less article's body.
            "ArticleTitle" | "ParagraphNum" | "ItemTitle" => self.sink = TextSink::Discard,
            "Paragraph" => {
                if let Some(article) = self.cur_article.as_mut() {
                    self.article_has_paragraph = true;
                    article.para_ordinal += 1;
                    let num = num
                        .filter(|n| !n.trim().is_empty())
                        .unwrap_or_else(|| article.para_ordinal.to_string());
                    let eid = format!("{}__para_{}", article.eid, normalize_num(&num));
                    let ordinal = self.next_ordinal();
                    self.cur_paragraph = Some(OpenParagraph {
                        eid,
                        num,
                        text: String::new(),
                        ordinal,
                        item_ordinal: 0,
                    });
                }
            }
            "ParagraphSentence" => self.sink = TextSink::ParagraphSentence,
            "Item" => {
                if let Some(para) = self.cur_paragraph.as_mut() {
                    para.item_ordinal += 1;
                    let num = num
                        .filter(|n| !n.trim().is_empty())
                        .unwrap_or_else(|| para.item_ordinal.to_string());
                    let eid = format!("{}__item_{}", para.eid, normalize_num(&num));
                    let ordinal = self.next_ordinal();
                    self.cur_item = Some(OpenItem {
                        eid,
                        num,
                        text: String::new(),
                        ordinal,
                        subitem_ordinal: 0,
                    });
                }
            }
            "ItemSentence" => self.sink = TextSink::ItemSentence,
            // Adjacent sentences within one node join with the 全角スペース; insert it
            // when a new <Sentence> opens on top of already-captured text (ADR-000013).
            "Sentence" => self.insert_sentence_join(),
            _ => {
                if is_subitem_sentence(name) {
                    self.sink = TextSink::SubitemSentence;
                } else if is_subitem_title(name) {
                    self.sink = TextSink::Discard;
                } else if let Some(level) = subitem_level(name) {
                    self.open_subitem(level, num);
                }
            }
        }
    }

    fn on_end(&mut self, name: &str) {
        match name {
            "SupplProvision" => self.in_suppl = false,
            "ArticleCaption" | "ParagraphSentence" | "ItemSentence" | "ArticleTitle"
            | "ParagraphNum" | "ItemTitle" => self.sink = TextSink::None,
            "Item" => {
                if let Some(item) = self.cur_item.take() {
                    let parent_eid = self
                        .cur_paragraph
                        .as_ref()
                        .map(|p| p.eid.clone())
                        .unwrap_or_default();
                    self.push(Node {
                        eid: item.eid,
                        node_type: NodeType::Item,
                        num: item.num,
                        sentence_text: item.text.trim().to_string(),
                        caption: String::new(),
                        parent_eid,
                        ordinal: item.ordinal,
                    });
                }
            }
            "Paragraph" => {
                if let Some(para) = self.cur_paragraph.take() {
                    let parent_eid = self
                        .cur_article
                        .as_ref()
                        .map(|a| a.eid.clone())
                        .unwrap_or_default();
                    self.push(Node {
                        eid: para.eid,
                        node_type: NodeType::Paragraph,
                        num: para.num,
                        sentence_text: para.text.trim().to_string(),
                        caption: String::new(),
                        parent_eid,
                        ordinal: para.ordinal,
                    });
                }
            }
            "Article" => {
                if let Some(article) = self.cur_article.take() {
                    // An article with paragraphs owns no direct sentence text; one
                    // without paragraphs owns its body text directly.
                    let sentence_text = if self.article_has_paragraph {
                        String::new()
                    } else {
                        article.body.trim().to_string()
                    };
                    self.push(Node {
                        eid: article.eid,
                        node_type: NodeType::Article,
                        num: article.num,
                        sentence_text,
                        caption: article.caption.trim().to_string(),
                        parent_eid: String::new(),
                        ordinal: article.ordinal,
                    });
                }
            }
            _ => {
                if is_subitem_sentence(name) || is_subitem_title(name) {
                    self.sink = TextSink::None;
                } else if subitem_level(name).is_some() {
                    self.close_subitem();
                }
            }
        }
    }

    fn on_text(&mut self, text: &str) {
        match self.sink {
            TextSink::Discard => {}
            TextSink::None => {
                // Text outside a recognized sink. If we are inside a paragraph-less
                // article (no open paragraph/item), this is the article's own body
                // text (e.g. a Sentence placed directly under the Article).
                if self.cur_paragraph.is_none()
                    && self.cur_item.is_none()
                    && !self.article_has_paragraph
                    && let Some(a) = self.cur_article.as_mut()
                {
                    a.body.push_str(text);
                }
            }
            TextSink::ArticleCaption => {
                if let Some(a) = self.cur_article.as_mut() {
                    a.caption.push_str(text);
                }
            }
            TextSink::ParagraphSentence => {
                if let Some(p) = self.cur_paragraph.as_mut() {
                    p.text.push_str(text);
                }
            }
            TextSink::ItemSentence => {
                if let Some(i) = self.cur_item.as_mut() {
                    i.text.push_str(text);
                }
            }
            TextSink::SubitemSentence => {
                if let Some(s) = self.cur_subitems.last_mut() {
                    s.text.push_str(text);
                }
            }
        }
    }

    /// On a new `<Sentence>` opening, push the canonical join separator if the active
    /// sink already holds text — so two `<Sentence>`s (incl. across `<Column>`s) join
    /// with one 全角スペース, matching the Go projector's `joined()`.
    fn insert_sentence_join(&mut self) {
        let buf = match self.sink {
            TextSink::ParagraphSentence => self.cur_paragraph.as_mut().map(|p| &mut p.text),
            TextSink::ItemSentence => self.cur_item.as_mut().map(|i| &mut i.text),
            TextSink::SubitemSentence => self.cur_subitems.last_mut().map(|s| &mut s.text),
            _ => None,
        };
        if let Some(buf) = buf
            && !buf.is_empty()
        {
            buf.push(SENTENCE_JOIN);
        }
    }

    /// Open a 号の細分. Its parent is the innermost open subitem, else the current item.
    fn open_subitem(&mut self, level: usize, num: Option<String>) {
        if self.cur_subitems.len() >= MAX_SUBITEM_DEPTH {
            // Deeper than any real 法令標準XML nests; skip the level (its end tag
            // is swallowed via skipped_subitem_depth) and degrade instead of
            // letting the eId prefix grow without bound.
            self.law.degraded = true;
            self.skipped_subitem_depth += 1;
            return;
        }
        let (parent_eid, default_num) = if let Some(top) = self.cur_subitems.last_mut() {
            top.child_ordinal += 1;
            (top.eid.clone(), top.child_ordinal)
        } else if let Some(item) = self.cur_item.as_mut() {
            item.subitem_ordinal += 1;
            (item.eid.clone(), item.subitem_ordinal)
        } else {
            // A subitem outside any item is malformed; flag degraded and skip it.
            self.law.degraded = true;
            return;
        };
        let num = num
            .filter(|n| !n.trim().is_empty())
            .unwrap_or_else(|| default_num.to_string());
        let eid = format!("{parent_eid}__subitem{level}_{}", normalize_num(&num));
        let ordinal = self.next_ordinal();
        self.cur_subitems.push(OpenSubitem {
            eid,
            num,
            text: String::new(),
            ordinal,
            child_ordinal: 0,
        });
    }

    /// Close the innermost open subitem, linking it to its parent (the next subitem on
    /// the stack, else the current item).
    fn close_subitem(&mut self) {
        if self.skipped_subitem_depth > 0 {
            // End tag of a level skipped for exceeding MAX_SUBITEM_DEPTH.
            self.skipped_subitem_depth -= 1;
            return;
        }
        if let Some(sub) = self.cur_subitems.pop() {
            let parent_eid = self
                .cur_subitems
                .last()
                .map(|s| s.eid.clone())
                .or_else(|| self.cur_item.as_ref().map(|i| i.eid.clone()))
                .unwrap_or_default();
            self.push(Node {
                eid: sub.eid,
                node_type: NodeType::Subitem,
                num: sub.num,
                sentence_text: sub.text.trim().to_string(),
                caption: String::new(),
                parent_eid,
                ordinal: sub.ordinal,
            });
        }
    }

    fn push(&mut self, node: Node) {
        if self.law.nodes.len() >= MAX_NODE_COUNT {
            // Stop accumulating beyond the per-document cap; the degraded flag
            // tells the classifier the node map is incomplete.
            self.law.degraded = true;
            return;
        }
        if self.law.nodes.insert(node.eid.clone(), node).is_some() {
            // Duplicate eId means a malformed law or a derivation bug — flag
            // degraded so the classifier drops to low confidence.
            self.law.degraded = true;
        }
    }

    fn finish(mut self) -> ParsedLaw {
        // Nothing should remain open in well-formed XML; if it is, mark degraded.
        if self.cur_article.is_some()
            || self.cur_paragraph.is_some()
            || self.cur_item.is_some()
            || !self.cur_subitems.is_empty()
        {
            self.law.degraded = true;
        }
        self.law
    }
}

/// Depth of a bare `<Subitem{n}>` element (Subitem1 → 1, Subitem10 → 10); `None` for
/// non-bare names such as `Subitem1Sentence` / `Subitem1Title`.
fn subitem_level(name: &str) -> Option<usize> {
    let rest = name.strip_prefix("Subitem")?;
    if rest.is_empty() || !rest.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    rest.parse().ok()
}

fn is_subitem_sentence(name: &str) -> bool {
    has_subitem_suffix(name, "Sentence")
}

fn is_subitem_title(name: &str) -> bool {
    has_subitem_suffix(name, "Title")
}

/// True for `Subitem{digits}{suffix}` (e.g. `Subitem2Sentence` with suffix "Sentence").
fn has_subitem_suffix(name: &str, suffix: &str) -> bool {
    name.strip_prefix("Subitem")
        .and_then(|r| r.strip_suffix(suffix))
        .is_some_and(|d| !d.is_empty() && d.bytes().all(|b| b.is_ascii_digit()))
}

#[cfg(test)]
mod tests {
    use std::fmt::Write as _;

    use super::*;

    /// A minimal law whose single item carries `depth` nested 号の細分 levels
    /// (`<Subitem1>` > `<Subitem2>` > …), each with a short Num.
    fn law_with_subitem_depth(depth: usize) -> String {
        let mut xml = String::from(
            r#"<Law><LawBody><MainProvision><Article Num="1"><Paragraph Num="1"><ParagraphSentence><Sentence>本文。</Sentence></ParagraphSentence><Item Num="1"><ItemSentence><Sentence>号。</Sentence></ItemSentence>"#,
        );
        for level in 1..=depth {
            write!(
                xml,
                "<Subitem{level} Num=\"1\"><Subitem{level}Sentence><Sentence>細分。</Sentence></Subitem{level}Sentence>"
            )
            .expect("write to String");
        }
        for level in (1..=depth).rev() {
            write!(xml, "</Subitem{level}>").expect("write to String");
        }
        xml.push_str("</Item></Paragraph></Article></MainProvision></LawBody></Law>");
        xml
    }

    fn subitem_count(law: &ParsedLaw) -> usize {
        law.nodes
            .values()
            .filter(|n| n.node_type == NodeType::Subitem)
            .count()
    }

    #[test]
    fn subitem_depth_at_cap_parses_cleanly() {
        let law = parse(law_with_subitem_depth(MAX_SUBITEM_DEPTH).as_bytes()).expect("parse");
        assert!(!law.degraded, "depth == cap must not degrade");
        assert_eq!(subitem_count(&law), MAX_SUBITEM_DEPTH);
    }

    #[test]
    fn subitem_depth_beyond_cap_degrades_and_stops_accumulating() {
        let law = parse(law_with_subitem_depth(MAX_SUBITEM_DEPTH + 8).as_bytes()).expect("parse");
        assert!(law.degraded, "depth beyond cap must degrade");
        assert_eq!(
            subitem_count(&law),
            MAX_SUBITEM_DEPTH,
            "levels beyond the cap must be dropped, not accumulated"
        );
        // The surrounding article/paragraph/item still materialize normally.
        assert!(law.nodes.contains_key("art_1__para_1__item_1"));
    }

    #[test]
    fn oversized_num_is_truncated_and_degrades() {
        let long_num = "9".repeat(MAX_NUM_LEN + 100);
        let xml = format!(
            r#"<Law><LawBody><MainProvision><Article Num="{long_num}"><Paragraph Num="1"><ParagraphSentence><Sentence>本文。</Sentence></ParagraphSentence></Paragraph></Article></MainProvision></LawBody></Law>"#
        );
        let law = parse(xml.as_bytes()).expect("parse");
        assert!(law.degraded, "oversized Num must degrade");

        let truncated_eid = format!("art_{}", "9".repeat(MAX_NUM_LEN));
        assert!(law.nodes.contains_key(&truncated_eid));
        // The truncated Num is what flows into descendant eIds, so they stay bounded.
        assert!(law.nodes.contains_key(&format!("{truncated_eid}__para_1")));
    }

    #[test]
    fn multibyte_num_at_boundary_truncates_on_char_boundary() {
        // 漢数字 Num: char-based truncation must not split a UTF-8 sequence (no panic).
        let long_num = "九".repeat(MAX_NUM_LEN + 1);
        let xml = format!(
            r#"<Law><LawBody><MainProvision><Article Num="{long_num}"/></MainProvision></LawBody></Law>"#
        );
        let law = parse(xml.as_bytes()).expect("parse");
        assert!(law.degraded);
        assert!(
            law.nodes
                .contains_key(&format!("art_{}", "九".repeat(MAX_NUM_LEN)))
        );
    }

    #[test]
    fn node_count_beyond_cap_stops_accumulating() {
        // Exercise the cap directly on the Builder: driving 1M+ nodes through the
        // XML reader would dominate the test suite's runtime for no extra coverage.
        let mut builder = Builder::default();
        for i in 0..(MAX_NODE_COUNT + 5) {
            let ordinal = builder.next_ordinal();
            builder.push(Node {
                eid: format!("art_{i}"),
                node_type: NodeType::Article,
                num: i.to_string(),
                sentence_text: String::new(),
                caption: String::new(),
                parent_eid: String::new(),
                ordinal,
            });
        }
        let law = builder.finish();
        assert!(law.degraded, "node count beyond cap must degrade");
        assert_eq!(law.nodes.len(), MAX_NODE_COUNT);
    }
}
