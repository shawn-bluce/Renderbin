import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from './http';
import { login, logout, register, setup } from './auth';

function response(init: { ok: boolean; body?: unknown }): Response {
	return {
		ok: init.ok,
		status: init.ok ? 200 : 401,
		json: async () => init.body ?? {},
		text: async () => JSON.stringify(init.body ?? {})
	} as unknown as Response;
}

function mockFetch(res: Response) {
	const fn = vi.fn().mockResolvedValue(res);
	vi.stubGlobal('fetch', fn);
	return fn;
}

afterEach(() => {
	vi.unstubAllGlobals();
});

describe('auth api', () => {
	it('login POSTs credentials and resolves on success', async () => {
		const fetchMock = mockFetch(response({ ok: true }));
		await expect(login('admin', 'pw')).resolves.toBeUndefined();
		expect(fetchMock).toHaveBeenCalledWith(
			'/api/auth/login',
			expect.objectContaining({
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ username: 'admin', password: 'pw' })
			})
		);
	});

	// The status is what the login page needs: 401 is a wrong password, while a
	// 403 means the password was right and the account is suspended, which the
	// page reports differently.
	it('login throws an ApiError carrying the status on failure', async () => {
		mockFetch(response({ ok: false }));
		await expect(login('admin', 'wrong')).rejects.toBeInstanceOf(ApiError);
		await expect(login('admin', 'wrong')).rejects.toMatchObject({ status: 401 });
	});

	it('login surfaces a 403 (disabled account) distinctly from a 401', async () => {
		const res = response({ ok: false });
		(res as { status: number }).status = 403;
		mockFetch(res);
		await expect(login('bob', 'right-password')).rejects.toMatchObject({ status: 403 });
	});

	it('logout POSTs /api/auth/logout and always resolves', async () => {
		const fetchMock = mockFetch(response({ ok: true }));
		await expect(logout()).resolves.toBeUndefined();
		expect(fetchMock).toHaveBeenCalledWith('/api/auth/logout', { method: 'POST' });
	});

	it('register POSTs the three fields and resolves on success', async () => {
		const fetchMock = mockFetch(response({ ok: true }));
		await expect(register('user', 'Nick', 'secret1')).resolves.toBeUndefined();
		expect(fetchMock).toHaveBeenCalledWith(
			'/api/auth/register',
			expect.objectContaining({
				method: 'POST',
				body: JSON.stringify({ username: 'user', nickname: 'Nick', password: 'secret1' })
			})
		);
	});

	it('register throws an ApiError carrying the status on failure', async () => {
		const res = response({ ok: false });
		(res as { status: number }).status = 409;
		mockFetch(res);
		await expect(register('user', 'Nick', 'secret1')).rejects.toMatchObject({ status: 409 });
		await expect(register('user', 'Nick', 'secret1')).rejects.toBeInstanceOf(ApiError);
	});

	it('setup POSTs the payload including config toggles', async () => {
		const fetchMock = mockFetch(response({ ok: true }));
		const payload = {
			username: 'boss',
			nickname: 'Boss',
			password: 'secret1',
			allow_registration: true,
			mcp_enabled: false
		};
		await expect(setup(payload)).resolves.toBeUndefined();
		expect(fetchMock).toHaveBeenCalledWith(
			'/api/setup',
			expect.objectContaining({ method: 'POST', body: JSON.stringify(payload) })
		);
	});
});
