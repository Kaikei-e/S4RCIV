<script lang="ts">
	// Provenance masthead (DESIGN_LANGUAGE §6 / ADR-000018). Site identity + nav, plus a
	// stance/provenance row. We state our posture and show *verifiability* — we never
	// self-claim trust ("verified ✓") and never imply active running (passive sentinel,
	// ADR-000014 / 設計原則①). Coverage + checkpoint render only when their data exists;
	// until then the row is just the stance line (no fake values).
	import { toJstShortLabelled } from '$lib/time';

	let {
		coverage = undefined,
		checkpoint = undefined
	}: {
		/** Number of Resources currently watched (control.watch count). */
		coverage?: number;
		/** Latest signed checkpoint, once the generator (ADR-000019) produces one. */
		checkpoint?: { seq: number; observedAt?: string; verifyHref?: string };
	} = $props();

	// Compose each ledger fragment as a single string so the visible text is one text
	// node (robust to text matchers; avoids {#if} splitting the run).
	const coverageLabel = $derived(
		coverage !== undefined ? `監視対象 ${coverage.toLocaleString()} 件` : ''
	);
	const checkpointLabel = $derived(
		checkpoint
			? `直近署名チェックポイント seq#${checkpoint.seq.toLocaleString()}` +
					(checkpoint.observedAt ? ` (${toJstShortLabelled(checkpoint.observedAt)})` : '')
			: ''
	);
</script>

<header class="masthead">
	<div class="ident">
		<a class="wordmark mono" href="/">S4RCIV</a>
		<span class="tagline">公的記録の観測ログ</span>
	</div>

	<nav class="nav" aria-label="サイトナビ">
		<a class="navlink" href="/votes" title="衆院の記名投票を小選挙区別に地図で見る">衆院</a>
		<a class="navlink" href="/sangiin" title="参院の記名投票を都道府県別に地図で見る">参院</a>
	</nav>

	<p class="ledger mono">
		<span class="stance">観測した事実を記録し、判断はしない</span>
		{#if coverageLabel}
			<span class="sep" aria-hidden="true">·</span><span class="cov">{coverageLabel}</span>
		{/if}
		{#if checkpointLabel}
			<span class="sep" aria-hidden="true">·</span><span class="chk">{checkpointLabel}</span>
			{#if checkpoint?.verifyHref}
				<a class="verify" href={checkpoint.verifyHref}>▸検証</a>
			{/if}
		{/if}
	</p>
</header>

<style>
	.masthead {
		display: grid;
		grid-template-columns: 1fr auto;
		grid-template-areas:
			'ident nav'
			'ledger ledger';
		align-items: center;
		gap: 6px 16px;
		padding: 12px 24px;
		border-bottom: 1px solid var(--hairline-2);
		background: var(--surface-1);
	}
	.ident {
		grid-area: ident;
		display: flex;
		align-items: baseline;
		gap: 12px;
		min-width: 0;
	}
	/* Wordmark: a mono logotype, deliberately NOT a status glyph (◉ etc. are reserved
	   for state — DESIGN_LANGUAGE §6 glyph reservation). */
	.wordmark {
		font-size: 18px;
		font-weight: 700;
		letter-spacing: 0.18em; /* editorial tracking (ledger, DESIGN_LANGUAGE §1) */
		color: var(--text-1);
		text-decoration: none;
		white-space: nowrap;
	}
	.wordmark:hover {
		color: var(--accent);
	}
	.tagline {
		font-size: 13px;
		color: var(--text-3);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.nav {
		grid-area: nav;
		display: flex;
		align-items: center;
		gap: 4px;
	}
	.navlink {
		font-size: 13px;
		padding: 4px 10px;
		border-radius: var(--r-sm);
		text-decoration: none;
		color: var(--text-2);
		white-space: nowrap;
	}
	.navlink:hover {
		color: var(--accent);
		background: var(--accent-weak);
	}
	.ledger {
		grid-area: ledger;
		margin: 0;
		font-size: 11px;
		line-height: 1.5;
		color: var(--text-3);
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 4px 8px;
	}
	.stance {
		letter-spacing: 0.04em;
	}
	.sep {
		color: var(--hairline-2);
	}
	.verify {
		color: var(--accent);
		text-decoration: none;
	}
	.verify:hover {
		text-decoration: underline;
	}
	/* Touch: enlarge nav targets to ≥44px (DESIGN_LANGUAGE §9.3 / WCAG 2.5.5). */
	@media (pointer: coarse) {
		.navlink,
		.verify {
			min-height: 44px;
			display: inline-flex;
			align-items: center;
		}
	}
	/* Narrow: stack identity over nav; drop the decorative tagline. */
	@media (max-width: 30rem) {
		.masthead {
			grid-template-columns: 1fr;
			grid-template-areas:
				'ident'
				'nav'
				'ledger';
			padding: 12px 16px;
		}
		.tagline {
			display: none;
		}
	}
</style>
