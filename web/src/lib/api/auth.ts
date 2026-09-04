export interface Me {
	id: number;
	username: string;
	nickname: string;
	is_admin: boolean;
}

export interface SetupStatus {
	needs_setup: boolean;
	allow_registration: boolean;
	max_file_size_bytes: number;
	public_share_base_url: string;
}

/** Error carrying the HTTP status so callers can map it to a message. */
export class AuthApiError extends Error {
	status: number;
	constructor(status: number, message: string) {
		super(message);
		this.status = status;
	}
}

export async function login(username: string, password: string): Promise<void> {
	const res = await fetch('/api/auth/login', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ username, password })
	});
	if (!res.ok) {
		// The status matters here: 401 is bad credentials, while 403 means the
		// password was right and the account is suspended — telling someone to
		// re-check their password in that case would send them in circles.
		throw new AuthApiError(res.status, await res.text());
	}
}

export async function register(
	username: string,
	nickname: string,
	password: string
): Promise<void> {
	const res = await fetch('/api/auth/register', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ username, nickname, password })
	});
	if (!res.ok) {
		throw new AuthApiError(res.status, await res.text());
	}
}

export interface SetupPayload {
	username: string;
	nickname: string;
	password: string;
	allow_registration: boolean;
	mcp_enabled: boolean;
}

export async function setup(payload: SetupPayload): Promise<void> {
	const res = await fetch('/api/setup', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(payload)
	});
	if (!res.ok) {
		throw new AuthApiError(res.status, await res.text());
	}
}

export async function logout(): Promise<void> {
	await fetch('/api/auth/logout', { method: 'POST' });
}

export async function me(): Promise<Me | null> {
	const res = await fetch('/api/auth/me');
	if (!res.ok) return null;
	return res.json();
}
