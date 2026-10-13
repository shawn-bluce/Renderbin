import { apiVoid } from './http';

export interface Me {
	id: number;
	username: string;
	nickname: string;
	is_admin: boolean;
}

export interface SetupStatus {
	needs_setup: boolean;
	allow_registration: boolean;
}

// The status on the ApiError matters to the login page: 401 is bad credentials,
// while 403 means the password was right and the account is suspended -- telling
// someone to re-check their password in that case would send them in circles.
export function login(username: string, password: string): Promise<void> {
	return apiVoid('/api/auth/login', { method: 'POST', json: { username, password } });
}

export function register(username: string, nickname: string, password: string): Promise<void> {
	return apiVoid('/api/auth/register', { method: 'POST', json: { username, nickname, password } });
}

export interface SetupPayload {
	username: string;
	nickname: string;
	password: string;
	allow_registration: boolean;
	mcp_enabled: boolean;
}

export function setup(payload: SetupPayload): Promise<void> {
	return apiVoid('/api/setup', { method: 'POST', json: payload });
}

/** Never throws: a failed logout leaves nothing the caller could do about it. */
export async function logout(): Promise<void> {
	await fetch('/api/auth/logout', { method: 'POST' });
}
