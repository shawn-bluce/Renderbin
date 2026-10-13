import { redirect } from '@sveltejs/kit';
import type { SetupStatus } from '$lib/api/auth';

export const ssr = false;

// First half of the SPA's guard: while the users table is empty every route
// funnels into /welcome (first-run setup), and once it is set up /welcome is a
// dead end. Whether the visitor is signed in is the (app) group's business --
// /login and /register live outside it and stay public.
export async function load({ url, fetch }) {
	let status: SetupStatus = { needs_setup: false, allow_registration: false };
	const statusRes = await fetch('/api/setup/status');
	if (statusRes.ok) {
		status = await statusRes.json();
	}

	if (status.needs_setup) {
		if (url.pathname !== '/welcome') redirect(302, '/welcome');
	} else if (url.pathname === '/welcome') {
		redirect(302, '/login');
	}
	return { allowRegistration: status.allow_registration };
}
