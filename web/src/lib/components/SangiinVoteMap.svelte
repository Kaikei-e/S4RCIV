<script lang="ts">
	// Choropleth of ONE 参議院 記名投票 over 都道府県 selection districts (1:N; ADR-000010).
	// A prefecture has multiple senators, so a single fill can't be one member's vote.
	// To avoid a 賛同率 heatmap (rejected as §3/§5-C scoring), the fill is a FACTUAL
	// category — 全員賛成 / 全員反対 / 割れ / 記録なし — and the raw 内訳 (賛成n/反対m) is shown
	// in context on click (§7). The boundary GeoJSON is a static basemap, not data.
	import { onMount, onDestroy } from 'svelte';
	import type { PrefectureTally } from '$lib/types';
	import { VOTE_COLORS, MAP_BASE } from '$lib/voteColors';

	let { prefectures = [] }: { prefectures?: PrefectureTally[] } = $props();

	let el: HTMLDivElement;
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let map: any;

	// Accessible equivalent of the choropleth's click popups (WCAG 1.1.1 / 2.1.1):
	// the table below reuses this directly, no GeoJSON fetch needed (unlike
	// DistrictVoteMap, PrefectureTally already carries districtName).
	const tableRows = $derived(
		[...prefectures].sort((a, b) => (a.districtName ?? '').localeCompare(b.districtName ?? '', 'ja'))
	);

	// Popup content as DOM nodes via textContent — never an HTML string. The current
	// inputs are static GeoJSON, but the same no-innerHTML rule as DistrictVoteMap
	// applies so a future data source can't introduce HTML injection here.
	function popupContent(title: string, lines: string[]): HTMLElement {
		const root = document.createElement('div');
		const strong = document.createElement('strong');
		strong.textContent = title;
		root.appendChild(strong);
		for (const line of lines) {
			root.appendChild(document.createElement('br'));
			root.appendChild(document.createTextNode(line));
		}
		return root;
	}

	// match ['get','id'] → factual category. id is the JIS prefecture code; a 合区 tally
	// ("31,32") is applied to both prefectures.
	function fillColorExpr(byCode: Map<number, PrefectureTally>): unknown {
		const groups: Record<'yes' | 'no' | 'split', number[]> = { yes: [], no: [], split: [] };
		for (const [code, t] of byCode) {
			const y = t.yes ?? 0;
			const n = t.no ?? 0;
			if (y > 0 && n === 0) groups.yes.push(code);
			else if (n > 0 && y === 0) groups.no.push(code);
			else if (y > 0 && n > 0) groups.split.push(code);
		}
		const expr: unknown[] = ['match', ['get', 'id']];
		for (const k of ['yes', 'no', 'split'] as const) {
			if (groups[k].length) expr.push(groups[k], VOTE_COLORS[k]);
		}
		expr.push(VOTE_COLORS.none);
		return expr.length > 3 ? expr : VOTE_COLORS.none;
	}

	onMount(async () => {
		const byCode = new Map<number, PrefectureTally>();
		for (const t of prefectures) {
			for (const c of (t.districtCode ?? '').split(',')) {
				const code = Number(c);
				if (code) byCode.set(code, t);
			}
		}

		const maplibregl = (await import('maplibre-gl')).default;
		await import('maplibre-gl/dist/maplibre-gl.css');

		map = new maplibregl.Map({
			container: el,
			style: {
				version: 8,
				sources: {},
				layers: [{ id: 'bg', type: 'background', paint: { 'background-color': MAP_BASE } }]
			},
			center: [137.5, 38.2],
			zoom: 4,
			attributionControl: false,
			// One finger scrolls the page, two fingers pan/zoom (DESIGN_LANGUAGE §9.4).
			cooperativeGestures: true,
			locale: {
				'CooperativeGesturesHandler.MobileHelpText': '2本指で地図を移動',
				'CooperativeGesturesHandler.WindowsHelpText': 'Ctrl + スクロールでズーム',
				'CooperativeGesturesHandler.MacHelpText': '⌘ + スクロールでズーム'
			}
		});
		map.addControl(new maplibregl.NavigationControl({ showCompass: false }), 'top-right');
		map.addControl(
			new maplibregl.AttributionControl({
				customAttribution: '境界: dataofjapan/land（都道府県）／簡略化: mapshaper'
			})
		);
		await new Promise<void>((resolve) => map.on('load', () => resolve()));

		map.addSource('pref', { type: 'geojson', data: '/geo/prefectures.geojson' });
		map.addLayer({
			id: 'fill',
			type: 'fill',
			source: 'pref',
			paint: { 'fill-color': fillColorExpr(byCode), 'fill-opacity': 0.82 }
		});
		map.addLayer({
			id: 'outline',
			type: 'line',
			source: 'pref',
			paint: { 'line-color': MAP_BASE, 'line-width': 0.4 }
		});

		const popup = new maplibregl.Popup({ closeButton: false });
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		map.on('click', 'fill', (e: any) => {
			const f = e.features?.[0];
			if (!f) return;
			const code = f.properties.id as number;
			const name = (f.properties.nam_ja as string) ?? String(code);
			const t = byCode.get(code);
			const body = t
				? `賛成 ${t.yes ?? 0} ／ 反対 ${t.no ?? 0}${t.abstain ? ` ／ 棄権・欠席 ${t.abstain}` : ''}`
				: '記録なし';
			popup.setLngLat(e.lngLat).setDOMContent(popupContent(name, [body])).addTo(map);
		});
		map.on('mouseenter', 'fill', () => (map.getCanvas().style.cursor = 'pointer'));
		map.on('mouseleave', 'fill', () => (map.getCanvas().style.cursor = ''));
	});

	onDestroy(() => map?.remove());
</script>

<div class="map" bind:this={el} role="img" aria-label="都道府県別の参議院記名投票地図"></div>

<!-- Accessible equivalent of the choropleth's click popups (WCAG 1.1.1 / 2.1.1):
     the map conveys per-prefecture 賛成/反対内訳 only via mouse click, so the same
     facts are also available here as a keyboard/screen-reader-navigable table.
     Collapsed by default (DESIGN_LANGUAGE §9.2 disclosure pattern). -->
{#if tableRows.length > 0}
	<details class="alt-table">
		<summary>都道府県別の一覧を表として表示（{tableRows.length} 件）</summary>
		<div class="scroll">
			<table>
				<thead>
					<tr>
						<th scope="col">都道府県</th>
						<th scope="col">賛成</th>
						<th scope="col">反対</th>
						<th scope="col">棄権・欠席</th>
					</tr>
				</thead>
				<tbody>
					{#each tableRows as row (row.districtCode)}
						<tr>
							<td>{row.districtName ?? '—'}</td>
							<td>{row.yes ?? 0}</td>
							<td>{row.no ?? 0}</td>
							<td>{row.abstain ?? 0}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	</details>
{/if}

<style>
	.alt-table {
		margin-top: 10px;
		font-size: 13px;
	}
	.alt-table summary {
		cursor: pointer;
		color: var(--text-2);
		padding: 6px 0;
	}
	.alt-table summary:hover {
		color: var(--accent);
	}
	.scroll {
		overflow-x: auto;
		max-height: 400px;
		overflow-y: auto;
		border: 1px solid var(--hairline-2);
		border-radius: var(--r-sm);
		margin-top: 6px;
	}
	table {
		width: 100%;
		border-collapse: collapse;
	}
	th,
	td {
		text-align: left;
		padding: 6px 10px;
		white-space: nowrap;
		border-bottom: 1px solid var(--hairline);
	}
	th {
		position: sticky;
		top: 0;
		background: var(--surface-2);
		color: var(--text-3);
		font-weight: 600;
		font-size: 12px;
	}
	.map {
		width: 100%;
		/* Shorter on phones so the map never fills the viewport (§9.4). */
		height: clamp(360px, 60vh, 520px);
		border-radius: 8px;
		overflow: hidden;
		border: 1px solid var(--hairline-2);
	}
	/* Popup themed to the dark token surface (§9.4) — was a light card before. */
	:global(.maplibregl-popup-content) {
		background: var(--surface-3);
		color: var(--text-1);
		border: 1px solid var(--hairline-2);
		border-radius: var(--r-sm);
		font-size: 13px;
		line-height: 1.5;
	}
	:global(.maplibregl-popup-tip) {
		display: none;
	}
</style>
