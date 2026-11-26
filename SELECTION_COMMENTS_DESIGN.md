# Selection toolbar: margin comments and "Explain with AI"

Select text in a document opened at `/view/{slug}` and a small toolbar offers
**Comment** and, when a model is configured, **Explain with AI**. Both end up as
notes floating over the right edge of the document, level with the passage they
are about. The "Ask AI" window is only for asking the model questions.

## How the selection and the passages cross the sandbox

`/res` documents run sandboxed in an opaque origin, so `/view` cannot read their
selection or layout. `selectionWidgetScript` (`handlers/askai.go`) is injected next
to the "Ask AI" link by `withViewWidgets` and works only inside `/view`'s frame
(`window.parent === window.top`). It talks to `/view` with `postMessage`:

- out: `{rb:'selection', action, text, rect}` when a toolbar button is clicked
  (text cut to 500 chars; rect is the selection box in the frame's viewport, which
  is `/view`'s viewport too because the frame starts at its top left);
  `{rb:'pointer'}` on a click in the document; `{rb:'scroll', y}`;
  `{rb:'measured', items:[{id, found, top}]}`; `{rb:'located', ok}`.
- in: `{rb:'measure', items:[{id, text}]}` (find those passages now and again on
  every layout change: ResizeObserver and load) and `{rb:'locate', text}` (scroll
  to the passage and select it).

Passages are found by searching the document's text with whitespace collapsed (text
nodes joined, a space between blocks), the way a selection's text arrives. A passage
that no longer exists is "not found", never mis-placed.

Messages are untrusted: the document's own scripts can post anything. `/view`
(`web/src/lib/selection.ts`) accepts only messages whose `source` is the frame's
window, validates every field (finite bounded numbers, bounded strings), and renders
text only. "Explain" sends a question to the model on arrival, so a document script
could trigger it on the viewer's behalf; it is limited to one per 3 s. Residual
risk: a hostile document can still make a signed-in viewer spend a request every few
seconds while the tab is open. Accepted for a personal instance.

The comment **input** is drawn by `/view` over the frame (not inside it), at the
selection's rect, so what is typed stays in the app origin.

## Who sees the link and toolbar

Documents: signed-in viewers, not non-owners of view-limited files (the frame loads
the document again, spending a view); sites: the owner's preview link only. Not
gated on a model: comments need none. The `/res` link reads "Ask AI" with a model
and "Comments" without; the "Explain with AI" button exists only with a model.

## Comments

Table `file_comments` (migration 0014, plus `is_ai` in 0015; no foreign keys or
cascade triggers, as for the chat, because `backup.Restore` copies tables with
DELETE+INSERT). Shared among everyone who may read the document, decided by
`GetChatFile` through `readableFile`. Endpoints: `GET/POST /api/comments/{slug}`,
`POST /api/comments/{slug}/explain`, `DELETE /api/comments/{slug}/{id}`.

- A top-level comment quotes the selected passage (required, <= 2000 bytes); a reply
  (one level, to a top-level comment of the same file) has no quote. Body <= 8 KB.
  No editing. The author shows as nickname (else username).
- The author and the document's owner may delete (others get 403); deleting a
  top-level comment deletes its replies.
- Anchoring is by quote only: no offsets stored. A note whose
  passage is gone waits in a small floating list ("no longer in the document"), with
  locate disabled; it is never lost to an edit.
- `cron.CollectChats` also removes comments whose file or author is gone, or whose
  parent is.

## Highlighting

Every commented passage the frame finds is marked in the document: a soft yellow
for comments, blue-grey for AI explanations. It uses the CSS Custom Highlight API
(`CSS.highlights`, named `rb-note` and `rb-note-ai`, styled by a `<style>` the
script adds), which marks text without touching the document's DOM; a browser
without it (before Chrome 105, Safari 17.2, Firefox 140) shows no highlight and
the notes work as before. A passage spanning several elements is highlighted
whole. It follows the comments: deleting one removes its highlight, and hiding the
notes (the "Comments" pill) clears them all, since `/view` then asks the frame to
measure nothing. Only the first occurrence of a repeated passage is marked, as with
locate. Not done: a stronger highlight for the note being hovered, and keeping the
selection marked while its comment is being typed.

## Explain with AI

`POST /api/comments/{slug}/explain {quote}` asks the model what the passage is,
using only the document: the system prompt (`explainSystemPrompt`) contains the
document and the selection, and says to find the passage, read its surroundings,
state plainly when the document does not make it clear, not guess, and not use
outside knowledge (no general-knowledge fallback, unlike the chat's prompt). The
answer is collected whole and stored as a comment with `is_ai` set, authored by the
reader who asked; it is shown with an AI badge, its Markdown sanitized like chat
answers. While it is fetched the note shows "Explaining...". Nothing is stored when
the model is unconfigured (409), refuses or is unreachable (502) or says nothing.
Whether the model actually declines when the document is silent depends on the
model; the prompt raises the odds, no test can guarantee it.

## Layout

Nothing is docked: the document keeps its full width. The notes (`MarginNotes`) float
over its right edge like the "Ask AI" button (`w-72`, no panel or background behind
them; only the cards take clicks). Each note's top is the passage's top in the
document (from `measured`) minus the scroll offset and the region's own offset;
overlapping notes are stacked in document order (`web/src/lib/notes-layout.ts`),
worked out in document coordinates so the stack does not shift while scrolling.
Notes whose passage is gone wait in a small floating list above the buttons. A
"Comments N" pill above the "Ask AI" one shows or hides all notes; they show by
themselves once there are some, and hiding them sticks. The "Ask AI" window floats
over the document, starts closed (opening a document is reading it), and opens from
its button at the bottom right.
