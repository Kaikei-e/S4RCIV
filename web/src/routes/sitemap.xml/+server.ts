import type { RequestHandler } from './$types';
import { listTimeline, listVoteEvents, listSangiinVoteEvents } from '$lib/server/queryClient';

// Static pages plus the most recently observed record pages (timeline items +
// current-session vote events). Not exhaustive over the whole history — that
// would be an unbounded, low-value crawl target — but enough for search
// engines to discover the site's structure and the freshest records without a
// prior link. robots.txt points here.
const STATIC_PATHS = ['/', '/votes', '/sangiin', '/about', '/terms', '/privacy', '/attribution'];

const RECENT_TIMELINE_ITEMS = 200;

export const GET: RequestHandler = async ({ url }) => {
	const origin = `https://${url.host}`;
	const paths = new Set(STATIC_PATHS);

	try {
		const timeline = await listTimeline({ pageSize: RECENT_TIMELINE_ITEMS });
		for (const item of timeline.items ?? []) {
			if (item.lawId) paths.add(`/laws/${item.lawId}`);
			if (item.issueId) paths.add(`/meetings/${item.issueId}`);
		}
	} catch (e) {
		console.error('[sitemap] listTimeline failed:', e);
	}

	try {
		const votes = await listVoteEvents(0);
		for (const v of votes.voteEvents ?? []) {
			if (v.voteEventId) paths.add(`/votes/${v.voteEventId}`);
		}
	} catch (e) {
		console.error('[sitemap] listVoteEvents failed:', e);
	}

	try {
		const sangiin = await listSangiinVoteEvents(0);
		for (const v of sangiin.voteEvents ?? []) {
			if (v.voteEventId) paths.add(`/sangiin/${v.voteEventId}`);
		}
	} catch (e) {
		console.error('[sitemap] listSangiinVoteEvents failed:', e);
	}

	const urls = [...paths]
		.map((path) => `  <url><loc>${origin}${path}</loc></url>`)
		.join('\n');
	const body = `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${urls}\n</urlset>\n`;

	return new Response(body, {
		headers: {
			'content-type': 'application/xml; charset=utf-8',
			'cache-control': 'public, max-age=3600'
		}
	});
};
