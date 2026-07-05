<script lang="ts">
	// Self-hosted fonts (DESIGN_LANGUAGE §11 / ADR-000020): no third-party request, so no
	// visitor-IP leak (passive sentinel, principle #1).
	// IBM Plex Mono — numbers / IDs / hashes / labels (latin subset, 400/500/600).
	import '@fontsource/ibm-plex-mono/latin-400.css';
	import '@fontsource/ibm-plex-mono/latin-500.css';
	import '@fontsource/ibm-plex-mono/latin-600.css';
	// IBM Plex Sans — latin runs inside sans body (latin subset, 400/500/600/700).
	import '@fontsource/ibm-plex-sans/latin-400.css';
	import '@fontsource/ibm-plex-sans/latin-500.css';
	import '@fontsource/ibm-plex-sans/latin-600.css';
	import '@fontsource/ibm-plex-sans/latin-700.css';
	// Japanese faces — IBM Plex Sans JP 400/600 (body/UI) + Zen Old Mincho 700 (record
	// titles, §4.1) — are self-hosted and unicode-range-SLICED (scripts/subset-fonts.mjs,
	// regenerate via `pnpm fonts:build`). The browser fetches only the slices whose glyphs
	// appear on the page (Google-Noto technique), so the homepage no longer pulls ~3.7 MB of
	// un-sliced CJK woff2 up front — the cause of the LCP=悪い regression. woff2 is already
	// Brotli, so cutting glyph count (not gzip) is the only lever that moves the bytes.
	import '$lib/fonts/jp.css';

	import '$lib/styles/tokens.css';
	import favicon from '$lib/assets/favicon.png';
	import Masthead from '$lib/components/Masthead.svelte';
	import SiteFooter from '$lib/components/SiteFooter.svelte';
	import { page } from '$app/state';
	import type { Snippet } from 'svelte';
	import type { LayoutData } from './$types';

	let { children, data }: { children: Snippet; data: LayoutData } = $props();

	// Absolute canonical URL for every page (SEO: dedupes ?query-carrying variants
	// of the same page, e.g. the filtered timeline). Forced https: regardless of
	// what scheme SvelteKit inferred internally behind the Cloudflare Tunnel —
	// this is an https-only public site, so there is no legitimate http canonical.
	const canonicalHref = $derived(`https://${page.url.host}${page.url.pathname}`);

	// Map the masthead provenance into the component's props. Coverage is always real
	// (control.watch count); the checkpoint lights up only once the generator (ADR-000019)
	// has written one. ▸検証 points at the public signed-checkpoint feed (passive exposure).
	const m = $derived(data?.masthead ?? null);
	const coverage = $derived(m?.watchCount != null ? Number(m.watchCount) : undefined);
	const checkpoint = $derived(
		m?.hasCheckpoint && m.checkpoint
			? {
					seq: Number(m.checkpoint.throughSeq ?? 0),
					observedAt: m.checkpoint.recordedAt,
					verifyHref: '/checkpoints'
				}
			: undefined
	);
</script>

<svelte:head>
	<link rel="icon" type="image/png" href={favicon} />
	<link rel="canonical" href={canonicalHref} />
	<meta property="og:site_name" content="S4RCIV" />
	<meta property="og:type" content="website" />
	<meta property="og:url" content={canonicalHref} />
	<meta name="twitter:card" content="summary" />
</svelte:head>

<a class="skip" href="#main">本文へスキップ</a>
<Masthead {coverage} {checkpoint} />
{@render children()}
<SiteFooter />

<style>
	/* Keyboard skip link (WCAG 2.4.1 / DESIGN_LANGUAGE §8). */
	.skip {
		position: absolute;
		left: -9999px;
		top: 0;
		z-index: 100;
		padding: 8px 12px;
		background: var(--surface-3);
		color: var(--text-1);
		border-radius: var(--r-sm);
	}
	.skip:focus {
		left: 8px;
		top: 8px;
	}
</style>
