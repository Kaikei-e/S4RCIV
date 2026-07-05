package egov

// JSON shapes of the e-Gov 法令 API v2 (and v1 updatelawlists) responses we read.
// Only the fields the egov-law adapter uses are typed.

// ── /laws (backfill) ─────────────────────────────────────────────────────────

type lawsResponse struct {
	TotalCount int        `json:"total_count"`
	Count      int        `json:"count"`
	NextOffset int        `json:"next_offset"`
	Laws       []lawEntry `json:"laws"`
}

type lawEntry struct {
	LawInfo             lawInfo             `json:"law_info"`
	RevisionInfo        revisionInfo        `json:"revision_info"`
	CurrentRevisionInfo currentRevisionInfo `json:"current_revision_info"`
}

type lawInfo struct {
	LawType          string `json:"law_type"`
	LawID            string `json:"law_id"`
	LawNum           string `json:"law_num"`
	PromulgationDate string `json:"promulgation_date"`
}

type revisionInfo struct {
	LawRevisionID            string `json:"law_revision_id"`
	LawTitle                 string `json:"law_title"`
	LawTitleKana             string `json:"law_title_kana"`
	Category                 string `json:"category"`
	AmendmentPromulgateDate  string `json:"amendment_promulgate_date"`
	AmendmentEnforcementDate string `json:"amendment_enforcement_date"`
	CurrentRevisionStatus    string `json:"current_revision_status"`
	RepealStatus             string `json:"repeal_status"`
	RepealDate               string `json:"repeal_date"`
}

type currentRevisionInfo struct {
	LawRevisionID            string `json:"law_revision_id"`
	LawTitle                 string `json:"law_title"`
	LawTitleKana             string `json:"law_title_kana"`
	Category                 string `json:"category"`
	AmendmentPromulgateDate  string `json:"amendment_promulgate_date"`
	AmendmentEnforcementDate string `json:"amendment_enforcement_date"`
	CurrentRevisionStatus    string `json:"current_revision_status"`
	RepealStatus             string `json:"repeal_status"`
	RepealDate               string `json:"repeal_date"`
}

// ── /law_data/{law_id} (snapshot) ────────────────────────────────────────────

type lawDataResponse struct {
	LawInfo      lawInfo      `json:"law_info"`
	RevisionInfo revisionInfo `json:"revision_info"`
	LawFullText  string       `json:"law_full_text"` // base64-encoded 法令標準XML
}

// ── /law_revisions/{law_id} (gap-recovery; v2) ───────────────────────────────
// Field names per the v2 OpenAPI spec (lawapi-v2.yaml); only the fields the
// gap-recovery path consumes are typed. Verify against a live response before
// relying on this in production (docs/refine/2026-07-06-egov-law-api-v2.md §5).

type lawRevisionsResponse struct {
	Revisions []lawRevisionEntry `json:"revisions"`
}

type lawRevisionEntry struct {
	LawRevisionID            string `json:"law_revision_id"`
	AmendmentPromulgateDate  string `json:"amendment_promulgate_date"`
	AmendmentEnforcementDate string `json:"amendment_enforcement_date"`
	AmendmentLawID           string `json:"amendment_law_id"`
}

// ── /updatelawlists/{yyyyMMdd} (re-poll; v1-shaped) ──────────────────────────
// The updated-law list nests entries under a top-level array (or object). e-Gov
// returns UpperCamelCase keys here; only the fields used are typed.

type updateLawEntry struct {
	LawID          string `json:"LawId"`
	EnforcementFlg string `json:"EnforcementFlg"` // "0" = 施行済 / "1" = 未施行
	AuthFlg        string `json:"AuthFlg"`        // "0" = 確認済 / "1" = 確認中
}
