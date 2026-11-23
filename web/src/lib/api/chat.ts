// "Ask AI": a signed-in reader's conversations with the configured model about
// one document. `code` is the access code of a public file the caller does not
// own, exactly as on /res; the owner passes none.
import { api, apiVoid, ApiError } from './http';

export interface ChatFile {
	slug: string;
	name: string;
	kind: string;
	owner: boolean;
}

export interface Conversation {
	id: number;
	title: string;
	created_at: string;
	updated_at: string;
}

export interface ChatMessage {
	id: number;
	role: 'user' | 'assistant';
	content: string;
	created_at: string;
}

export interface ChatOverview {
	file: ChatFile;
	ai_enabled: boolean;
	conversations: Conversation[];
}

function chatPath(slug: string, sub: string, code: string | null): string {
	const path = `/api/chat/${encodeURIComponent(slug)}${sub}`;
	return code ? `${path}?code=${encodeURIComponent(code)}` : path;
}

export function getChatOverview(
	slug: string,
	code: string | null,
	f?: typeof fetch
): Promise<ChatOverview> {
	return api(chatPath(slug, '', code), { fetch: f });
}

export function getConversation(
	slug: string,
	code: string | null,
	id: number
): Promise<{ conversation: Conversation; messages: ChatMessage[] }> {
	return api(chatPath(slug, `/conversations/${id}`, code));
}

export function deleteConversation(slug: string, code: string | null, id: number): Promise<void> {
	return apiVoid(chatPath(slug, `/conversations/${id}`, code), { method: 'DELETE' });
}

/** One event of the answer stream; see ChatHandler.Send on the server. */
export type ChatEvent =
	| { type: 'start'; conversation: Conversation; message: ChatMessage }
	| { type: 'delta'; text: string }
	| { type: 'done'; message: ChatMessage | null }
	| { type: 'error'; error: string; message: ChatMessage | null };

/**
 * Asks the model and calls `onEvent` for each event as it arrives. A failure
 * before the answer starts (bad request, AI off, the provider refusing) throws
 * an ApiError and stored nothing; one after `start` arrives as an `error`
 * event. Aborting `signal` stops the answer; what was written so far is kept by
 * the server.
 */
export async function sendChatMessage(
	slug: string,
	code: string | null,
	body: { conversation_id: number | null; content: string },
	onEvent: (ev: ChatEvent) => void,
	signal: AbortSignal
): Promise<void> {
	const res = await fetch(chatPath(slug, '/messages', code), {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ conversation_id: body.conversation_id ?? 0, content: body.content }),
		signal
	});
	if (!res.ok) {
		throw new ApiError(res.status, (await res.text()).trim());
	}
	if (!res.body) return;
	const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
	let buffer = '';
	for (;;) {
		const { value, done } = await reader.read();
		if (done) break;
		buffer += value;
		let nl: number;
		while ((nl = buffer.indexOf('\n')) >= 0) {
			const line = buffer.slice(0, nl).trim();
			buffer = buffer.slice(nl + 1);
			if (line) onEvent(JSON.parse(line) as ChatEvent);
		}
	}
	if (buffer.trim()) onEvent(JSON.parse(buffer) as ChatEvent);
}
