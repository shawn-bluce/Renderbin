import { error } from '@sveltejs/kit';
import { ApiError } from '$lib/api/http';
import { getChatOverview } from '$lib/api/chat';

// /view/{slug}[?code=...]: a document with "Ask AI" beside it. The "Ask AI"
// button on a /res page leads here. The (app) layout has already required a
// signed-in account; the server decides whether that account may read this
// document (its owner, or a holder of a public file's code) and answers 404
// otherwise, as /res would.
export async function load({ fetch, params, url }) {
	const code = url.searchParams.get('code');
	try {
		const overview = await getChatOverview(params.slug, code, fetch);
		return { slug: params.slug, code, overview };
	} catch (err) {
		if (err instanceof ApiError && err.status === 404) error(404, 'Not found');
		throw err;
	}
}
