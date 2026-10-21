import { listFilesPage } from '$lib/api/files';
import { getUsage } from '$lib/api/settings';

// The first page and the storage figure are independent, so they load side by
// side. A failed usage call is not worth failing the page over: the meter just
// stays empty until the next action refreshes it.
export async function load({ fetch }) {
	const [page, usage] = await Promise.all([
		listFilesPage({ sort: 'recent', visibility: 'all' }, fetch),
		getUsage(fetch).catch(() => null)
	]);
	return { page, usage };
}
