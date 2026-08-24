import adapter from '@sveltejs/adapter-node';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	compilerOptions: {
		// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
		runes: ({ filename }) => (filename.split(/[/\\]/).includes('node_modules') ? undefined : true)
	},
	kit: {
		adapter: adapter(),
		// Content-Security-Policy as defense-in-depth (CWE-1021/CWE-693). Everything is
		// same-origin: the map style is inline and its GeoJSON basemap is served from
		// /geo, so no remote script/style/tile/connect origins are needed. 'auto' mode
		// lets SvelteKit nonce/hash its own inline bootstrap under script-src 'self'.
		// maplibre-gl v6 self-hosts its worker (see $lib/maplibre) — the URL is
		// same-origin, so it is constructed directly and worker-src 'self' suffices
		// (v5 needed blob:). It still injects inline element styles, hence style-src
		// 'unsafe-inline'. frame-ancestors 'none' blocks clickjacking.
		csp: {
			mode: 'auto',
			directives: {
				'default-src': ['self'],
				'script-src': ['self'],
				'style-src': ['self', 'unsafe-inline'],
				'img-src': ['self', 'data:', 'blob:'],
				'font-src': ['self'],
				'connect-src': ['self'],
				'worker-src': ['self'],
				'child-src': ['self'],
				'frame-ancestors': ['none'],
				'base-uri': ['self'],
				'form-action': ['self'],
				'object-src': ['none']
			}
		}
	}
};

export default config;
