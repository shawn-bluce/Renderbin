// Comments on a document: shared among everyone who may read it. `code` is the
// access code of a public file the caller does not own, exactly as on /res; the
// owner passes none. See CommentsHandler on the server.
import { api, apiVoid } from './http';

export interface Comment {
	id: number;
	/** 0 for a top-level comment, else the id of the comment this replies to. */
	parent_id: number;
	/** The passage a top-level comment is about; empty for a reply. */
	quote: string;
	body: string;
	/** The body is the model's explanation of the quote, requested by `author`. */
	is_ai: boolean;
	author: string;
	mine: boolean;
	can_delete: boolean;
	created_at: string;
}

function path(slug: string, sub: string, code: string | null): string {
	const p = `/api/comments/${encodeURIComponent(slug)}${sub}`;
	return code ? `${p}?code=${encodeURIComponent(code)}` : p;
}

export async function listComments(slug: string, code: string | null): Promise<Comment[]> {
	return (await api<{ comments: Comment[] }>(path(slug, '', code))).comments;
}

export function createComment(
	slug: string,
	code: string | null,
	body: { parent_id?: number; quote?: string; body: string }
): Promise<Comment> {
	return api(path(slug, '', code), { method: 'POST', json: body });
}

/** Deletes a comment and its replies. */
export function deleteComment(slug: string, code: string | null, id: number): Promise<void> {
	return apiVoid(path(slug, `/${id}`, code), { method: 'DELETE' });
}

/**
 * Asks the model what `quote` is, from the document alone, and returns the
 * answer stored as an AI comment on it. Waits for the whole answer.
 */
export function explainComment(slug: string, code: string | null, quote: string): Promise<Comment> {
	return api(path(slug, '/explain', code), { method: 'POST', json: { quote } });
}
