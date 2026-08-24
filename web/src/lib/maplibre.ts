// Lazy loader for maplibre-gl, shared by every map component.
//
// MapLibre v6 is ESM-only and no longer has a default export, so the module
// namespace itself is the API surface (`import * as maplibregl`). Under a
// bundler it also needs an explicit `setWorkerUrl()`: the worker resolves its
// own URL from `import.meta.url`, which does not point at the emitted worker
// chunk once Vite has rewritten the module graph.
//
// The `?worker&url` query (not plain `?url`) routes the file through Vite's
// worker pipeline, so the emitted chunk is self-contained. With plain `?url`
// the dist worker is copied verbatim and its `maplibre-gl-shared.mjs` sibling
// import fails at runtime in production builds.
//
// The resulting URL is same-origin, so maplibre constructs the worker directly
// instead of laundering it through a Blob — `worker-src 'self'` in the CSP
// (svelte.config.js) is enough.
export async function loadMapLibre() {
	const maplibregl = await import('maplibre-gl');
	const { default: workerUrl } = await import(
		'maplibre-gl/dist/maplibre-gl-worker.mjs?worker&url'
	);
	maplibregl.setWorkerUrl(workerUrl);
	await import('maplibre-gl/dist/maplibre-gl.css');
	return maplibregl;
}
