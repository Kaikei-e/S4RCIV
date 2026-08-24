<script lang="ts">
	// Choropleth of ONE 記名投票 over the 衆 small-electoral-districts (ADR-000008).
	// Colour encodes a factual vote category only (賛成/反対/棄権/記録なし) — never an
	// aggregate score (§3/§5-C). The boundary GeoJSON is a static basemap (国土数値情報),
	// not observed data; the facts (district → option) come from the API with provenance.
	// Districts join the basemap by `kucode` (== ken*100+ku). 比例 members carry no
	// district and are shown by the page's companion panel, never erased (§5).
	import { onMount, onDestroy } from 'svelte';
	import type { Vote } from '$lib/types';
	import { VOTE_COLORS, MAP_BASE } from '$lib/voteColors';
	import { loadMapLibre } from '$lib/maplibre';

	let { votes = [] }: { votes?: Vote[] } = $props();

	let el: HTMLDivElement;
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	let map: any;

	const OPT_JA: Record<string, string> = { yes: '賛成', no: '反対', abstain: '棄権' };

	// district_code (== GeoJSON kucode) → the sitting member's recorded vote.
	// Shared by the map fill/popups and the accessible table below (WCAG 1.1.1 /
	// 2.1.1 — the choropleth's per-district facts must not be mouse-only).
	const byDistrict = $derived.by(() => {
		const m = new Map<number, Vote>();
		for (const v of votes) {
			if (!v.isPr && v.districtCode) m.set(Number(v.districtCode), v);
		}
		return m;
	});

	// kucode → district display name. Only the basemap GeoJSON carries names (the
	// API's Vote records don't), so it is fetched directly here — independent of
	// MapLibre's own internal fetch/render state — so the table below is complete
	// even before/without the map having rendered those features.
	let districtNames = $state<Map<number, string>>(new Map());

	const tableRows = $derived(
		[...districtNames.entries()]
			.map(([kucode, name]) => ({ kucode, name, vote: byDistrict.get(kucode) }))
			.sort((a, b) => a.kucode - b.kucode)
	);

	// Popup content as DOM nodes via textContent — never an HTML string. Member /
	// district names are upstream-derived, so they must not reach innerHTML (XSS).
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

	// A MapLibre `match` expression colouring each district by its member's option.
	// Empty option groups are skipped (a match needs ≥1 label/output pair); when no
	// district has a record at all, fall back to a flat "no record" colour.
	function fillColorExpr(byDistrict: Map<number, Vote>): unknown {
		const groups: Record<string, number[]> = { yes: [], no: [], abstain: [] };
		for (const [kucode, v] of byDistrict) {
			const g = groups[v.option ?? ''];
			if (g) g.push(kucode);
		}
		const expr: unknown[] = ['match', ['get', 'kucode']];
		for (const opt of ['yes', 'no', 'abstain']) {
			if (groups[opt].length) expr.push(groups[opt], VOTE_COLORS[opt]);
		}
		expr.push(VOTE_COLORS.none); // default = 記録なし
		return expr.length > 3 ? expr : VOTE_COLORS.none;
	}

	onMount(async () => {
		fetch('/geo/senkyoku289.geojson')
			.then((r) => r.json())
			.then((geo) => {
				const m = new Map<number, string>();
				for (const f of geo.features ?? []) {
					const kucode = f.properties?.kucode;
					const kuname = f.properties?.kuname;
					if (kucode != null && kuname) m.set(Number(kucode), String(kuname));
				}
				districtNames = m;
			})
			.catch(() => {}); // the table degrades to empty; the map itself is unaffected

		const maplibregl = await loadMapLibre();

		map = new maplibregl.Map({
			container: el,
			// Blank style — deliberately NO third-party basemap tiles (passive /
			// self-hosted ethos): the districts themselves are the map.
			style: {
				version: 8,
				sources: {},
				layers: [{ id: 'bg', type: 'background', paint: { 'background-color': MAP_BASE } }]
			},
			center: [137.5, 38.2],
			zoom: 4,
			attributionControl: false,
			// Don't trap page scroll on touch (DESIGN_LANGUAGE §9.4): one finger
			// scrolls the page, two fingers pan/zoom. Hint text localised to JA.
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
				customAttribution: '境界: 国土数値情報（国土交通省）／加工: SmartNews Media Research Institute'
			})
		);

		await new Promise<void>((resolve) => map.on('load', () => resolve()));

		map.addSource('districts', {
			type: 'geojson',
			data: '/geo/senkyoku289.geojson',
			promoteId: 'kucode'
		});
		map.addLayer({
			id: 'fill',
			type: 'fill',
			source: 'districts',
			paint: { 'fill-color': fillColorExpr(byDistrict), 'fill-opacity': 0.82 }
		});
		map.addLayer({
			id: 'outline',
			type: 'line',
			source: 'districts',
			paint: { 'line-color': MAP_BASE, 'line-width': 0.4 }
		});

		const popup = new maplibregl.Popup({ closeButton: false });
		// eslint-disable-next-line @typescript-eslint/no-explicit-any
		map.on('click', 'fill', (e: any) => {
			const f = e.features?.[0];
			if (!f) return;
			const kucode = f.properties.kucode as number;
			const v = byDistrict.get(kucode);
			const name = (f.properties.kuname as string) ?? String(kucode);
			const opt = v ? (OPT_JA[v.option ?? ''] ?? '—') : '記録なし';
			const who = v?.voterName ? `${v.voterName}${v.parliamentaryGroup ? `（${v.parliamentaryGroup}）` : ''}` : '';
			popup
				.setLngLat(e.lngLat)
				.setDOMContent(popupContent(name, who ? [who, `投票: ${opt}`] : [`投票: ${opt}`]))
				.addTo(map);
		});
		map.on('mouseenter', 'fill', () => (map.getCanvas().style.cursor = 'pointer'));
		map.on('mouseleave', 'fill', () => (map.getCanvas().style.cursor = ''));
	});

	onDestroy(() => map?.remove());
</script>

<div class="map" bind:this={el} role="img" aria-label="選挙区別の記名投票地図"></div>

<!-- Accessible equivalent of the choropleth's click popups (WCAG 1.1.1 / 2.1.1):
     the map conveys district → member → vote only via mouse hover/click, so the
     same facts are also available here as a keyboard/screen-reader-navigable
     table. Collapsed by default (DESIGN_LANGUAGE §9.2 disclosure pattern) since
     289 rows would otherwise dominate the page. -->
{#if tableRows.length > 0}
	<details class="alt-table">
		<summary>選挙区別の一覧を表として表示（{tableRows.length} 区）</summary>
		<div class="scroll">
			<table>
				<thead>
					<tr>
						<th scope="col">選挙区</th>
						<th scope="col">議員</th>
						<th scope="col">会派</th>
						<th scope="col">投票</th>
					</tr>
				</thead>
				<tbody>
					{#each tableRows as row (row.kucode)}
						<tr>
							<td>{row.name}</td>
							<td>{row.vote?.voterName ?? '—'}</td>
							<td>{row.vote?.parliamentaryGroup ?? '—'}</td>
							<td class="opt-{row.vote?.option ?? 'none'}"
								>{row.vote ? (OPT_JA[row.vote.option ?? ''] ?? '—') : '記録なし'}</td
							>
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
	/* Factual category colours, keyed to the map fill (DESIGN_LANGUAGE §6). */
	.opt-yes {
		color: var(--dv-1);
	}
	.opt-no {
		color: var(--dv-2);
	}
	.opt-abstain {
		color: var(--dv-4);
	}
	.opt-none {
		color: var(--text-3);
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
