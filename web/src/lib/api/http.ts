// The one place that talks to `fetch` for the JSON API: it builds the request,
// turns a non-2xx answer into an ApiError, and decodes the body. The endpoint
// modules next to this one are one-liners over it.

/**
 * A failed API call. Keeps the HTTP status so callers can map known failures
 * (a 409 slug conflict, a 403 suspended account) to translated messages
 * instead of showing the backend's raw English text.
 */
export class ApiError extends Error {
	constructor(
		public readonly status: number,
		message: string
	) {
		super(message);
		this.name = 'ApiError';
	}
}

export interface RequestOptions {
	method?: string;
	/** A value to send as a JSON body. */
	json?: unknown;
	/** A raw body (a zip, a database snapshot) and its Content-Type. */
	body?: BodyInit;
	type?: string;
	/** `fetch` is overridable so SvelteKit `load` functions can pass their own. */
	fetch?: typeof fetch;
}

async function send(path: string, o: RequestOptions): Promise<Response> {
	const init: RequestInit = {};
	if (o.method) init.method = o.method;
	if (o.json !== undefined) {
		init.headers = { 'Content-Type': 'application/json' };
		init.body = JSON.stringify(o.json);
	} else if (o.body !== undefined) {
		if (o.type) init.headers = { 'Content-Type': o.type };
		init.body = o.body;
	}
	const res = await (o.fetch ?? fetch)(path, ...(Object.keys(init).length ? [init] : []));
	if (!res.ok) {
		// http.Error on the backend appends a trailing newline -- trim it off.
		throw new ApiError(res.status, (await res.text()).trim());
	}
	return res;
}

/** Calls the API and decodes the JSON answer. */
export async function api<T>(path: string, o: RequestOptions = {}): Promise<T> {
	return (await send(path, o)).json();
}

/** Calls the API for its effect only: the answer's body, if any, is ignored. */
export async function apiVoid(path: string, o: RequestOptions = {}): Promise<void> {
	await send(path, o);
}
