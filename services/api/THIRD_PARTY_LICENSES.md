# Third-Party Licenses

The `services/api` Go module bundles the third-party components below. The S4RCIV
server body is **AGPL-3.0**; every component here is under a permissive license
**compatible with AGPL-3.0**, with attribution retained as required. (Researched and
recorded per ADR-000008 / the M4 voter-name segmentation work, 2026-06-03.)

## Japanese morphological analyzer (voter-name segmentation)

Used only to segment free-text 記名投票 rosters in `internal/domain/legislative`
(`vote_names.go`); the structured 選挙区 field stays on a deterministic parser.

| Component | Version | License | Copyright / Notice |
|---|---|---|---|
| `github.com/ikawaha/kagome/v2` | v2.11.0 | **MIT** | © ikawaha. Pure-Go MeCab-equivalent morphological analyzer; no cgo. |
| `github.com/ikawaha/kagome-dict` | v1.1.7 | **MIT** | © ikawaha. Dictionary packaging for Kagome v2. |
| `github.com/ikawaha/kagome-dict/ipa` | v1.2.6 | **MIT** (packaging) | © ikawaha. Embeds MeCab-IPADIC data — see below. |
| MeCab-IPADIC (data embedded by `…/ipa`) | mecab-ipadic-2.7.0-20070801 | **BSD-style (ICOT/NAIST)** | © Nara Institute of Science and Technology; derived from ICOT Free Software. |

**MeCab-IPADIC terms (preserved as required):** the dictionary may be freely
redistributed in original or modified form provided the original copyright notice
and the **NO WARRANTY** disclaimer are retained, and use complies with applicable
law. The canonical `COPYING` text ships inside the `kagome-dict/ipa` module. The
license is recognized as DFSG-free (Debian *main*) and is compatible with inclusion
in a GPL/AGPL application.

> **UniDic (`kagome-dict/uni`) is deliberately NOT used.** It carries a
> GPL-2.0-only OR LGPL-2.1-only OR BSD-3-Clause triple license (the GPL-2.0-only
> arm is *incompatible* with AGPL-3.0) and is far larger. If it is ever adopted, it
> must be taken under its **BSD-3-Clause** arm, with that choice recorded here.

## Go supplementary libraries

| Component | Version | License | Use |
|---|---|---|---|
| `golang.org/x/net` (`/html`) | v0.58.0 | **BSD-3-Clause** (© The Go Authors) | HTML table parsing of the 両院公式議員名簿 (giin-roster gateway). |
| `golang.org/x/text` (`/encoding/japanese`) | v0.41.0 | **BSD-3-Clause** (© The Go Authors) | Shift_JIS → UTF-8 decoding of the roster pages. |

## AGPL-3.0 compatibility conclusion

MIT and BSD-3-Clause are permissive and one-way compatible into AGPL-3.0. MeCab-IPADIC
is a permissive BSD-style license whose only obligations (retain copyright + NO
WARRANTY notice) are satisfied by this file plus the bundled `COPYING`. No copyleft
obligation conflicts with the AGPL-3.0 server body.
