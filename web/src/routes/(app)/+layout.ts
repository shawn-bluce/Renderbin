import { redirect } from '@sveltejs/kit';
import type { Me } from '$lib/api/auth';

// Second half of the guard, for every page that needs an account: the dashboard
// and settings. The page below can rely on `user` being a signed-in account.
export async function load({ fetch, parent }) {
	// Wait for the setup check first: on a fresh install /api/auth/me is a 401
	// too, and "go to /welcome" has to win over "go to /login".
	await parent();
	const res = await fetch('/api/auth/me');
	if (!res.ok) {
		redirect(302, '/login');
	}
	const user: Me = await res.json();
	return { user };
}
