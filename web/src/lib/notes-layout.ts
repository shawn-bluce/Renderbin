// Where margin notes sit. Each note wants to be level with the passage it is
// about; notes that would overlap are pushed down, in document order, so a busy
// paragraph gets a tidy stack instead of a pile. The stacking is worked out in
// document coordinates, so it does not change as the document scrolls; the page
// only subtracts the scroll offset.

export interface NoteBox {
	/** Any stable key. */
	key: string;
	/** Distance of the passage from the top of the document. */
	anchor: number;
	/** The note's rendered height. */
	height: number;
}

/** The top of each note, in document coordinates. */
export function layoutNotes(notes: NoteBox[], gap = 8): Map<string, number> {
	const sorted = [...notes].sort((a, b) => a.anchor - b.anchor);
	const tops = new Map<string, number>();
	let bottom = -Infinity;
	for (const n of sorted) {
		const top = Math.max(n.anchor, bottom + gap);
		tops.set(n.key, top);
		bottom = top + n.height;
	}
	return tops;
}
