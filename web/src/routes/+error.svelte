<script lang="ts">
	// Branded error boundary (404 / API-down 502, etc.) — renders inside the root
	// +layout.svelte, so the masthead/footer are already present around this.
	// Previously there was no +error.svelte, so any dead link or backend outage
	// fell through to SvelteKit's unstyled default page (English chrome, no way
	// back to the timeline) — a visible dead end on a service whose credibility
	// is the product.
	import { page } from '$app/state';

	const status = $derived(page.status);
	const isNotFound = $derived(status === 404);
</script>

<svelte:head>
	<title>{status} — S4RCIV</title>
</svelte:head>

<main id="main" class="wrap">
	<p class="code mono">{status}</p>
	<p class="message">
		{#if isNotFound}
			お探しのページは見つかりませんでした。
		{:else}
			一時的にページを表示できませんでした。
		{/if}
	</p>
	<p class="detail">
		{#if isNotFound}
			URL が変更されたか、削除された可能性があります。
		{:else}
			サーバとの通信に問題が発生しています。しばらくしてから再度お試しください。
		{/if}
	</p>
	<a class="home" href="/">タイムラインへ戻る</a>
</main>

<style>
	.wrap {
		max-width: 480px;
		margin: 0 auto;
		padding: 64px 24px;
		text-align: center;
	}
	.code {
		font-size: 13px;
		letter-spacing: 0.08em;
		color: var(--st-critical-t);
		margin: 0 0 12px;
	}
	.message {
		font-size: 18px;
		font-weight: 600;
		color: var(--text-1);
		margin: 0 0 8px;
	}
	.detail {
		font-size: 14px;
		color: var(--text-3);
		margin: 0 0 28px;
	}
	.home {
		display: inline-flex;
		align-items: center;
		font-size: 14px;
		padding: 8px 16px;
		border: 1px solid var(--hairline-2);
		border-radius: var(--r-sm);
		text-decoration: none;
		color: var(--text-2);
	}
	.home:hover {
		color: var(--accent);
		border-color: var(--accent);
	}
	/* Touch: enlarge the link target to ≥44px (DESIGN_LANGUAGE §9.3 / WCAG 2.5.5). */
	@media (pointer: coarse) {
		.home {
			min-height: 44px;
		}
	}
</style>
