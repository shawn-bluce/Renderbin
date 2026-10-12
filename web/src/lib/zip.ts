// A minimal zip writer: "store" only (no compression), UTF-8 names, no
// dependency. It exists so a folder picked in the browser can travel to
// POST /api/sites as one request body; the server re-packs it with deflate, so
// compressing here would only cost CPU on the client.

export interface ZipEntry {
	/** Slash-separated path inside the archive. */
	path: string;
	data: Uint8Array;
}

const CRC_TABLE = (() => {
	const table = new Uint32Array(256);
	for (let n = 0; n < 256; n++) {
		let c = n;
		for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
		table[n] = c >>> 0;
	}
	return table;
})();

/** CRC-32 (IEEE), as zip requires. */
export function crc32(data: Uint8Array): number {
	let c = 0xffffffff;
	for (let i = 0; i < data.length; i++) c = CRC_TABLE[(c ^ data[i]) & 0xff] ^ (c >>> 8);
	return (c ^ 0xffffffff) >>> 0;
}

// General-purpose flag bit 11: the name is UTF-8. Without it, unzip tools
// (and the server) would have to guess the encoding of a non-ASCII name.
const FLAG_UTF8 = 0x0800;
// 1980-01-01 00:00 in MS-DOS format, the earliest representable date. A fixed
// stamp keeps the output deterministic; the server ignores it anyway.
const DOS_TIME = 0;
const DOS_DATE = (0 << 9) | (1 << 5) | 1;

/** Builds a zip archive holding entries uncompressed. */
export function buildZip(entries: ZipEntry[]): Uint8Array<ArrayBuffer> {
	const encoder = new TextEncoder();
	const locals: Uint8Array[] = [];
	const centrals: Uint8Array[] = [];
	let offset = 0;

	for (const entry of entries) {
		const name = encoder.encode(entry.path);
		const crc = crc32(entry.data);
		const size = entry.data.length;

		const local = new Uint8Array(30 + name.length);
		const lv = new DataView(local.buffer);
		lv.setUint32(0, 0x04034b50, true); // local file header signature
		lv.setUint16(4, 20, true); // version needed to extract (2.0)
		lv.setUint16(6, FLAG_UTF8, true);
		lv.setUint16(8, 0, true); // method: store
		lv.setUint16(10, DOS_TIME, true);
		lv.setUint16(12, DOS_DATE, true);
		lv.setUint32(14, crc, true);
		lv.setUint32(18, size, true); // compressed size
		lv.setUint32(22, size, true); // uncompressed size
		lv.setUint16(26, name.length, true);
		lv.setUint16(28, 0, true); // extra field length
		local.set(name, 30);

		const central = new Uint8Array(46 + name.length);
		const cv = new DataView(central.buffer);
		cv.setUint32(0, 0x02014b50, true); // central directory header signature
		cv.setUint16(4, 20, true); // version made by
		cv.setUint16(6, 20, true); // version needed to extract
		cv.setUint16(8, FLAG_UTF8, true);
		cv.setUint16(10, 0, true); // method: store
		cv.setUint16(12, DOS_TIME, true);
		cv.setUint16(14, DOS_DATE, true);
		cv.setUint32(16, crc, true);
		cv.setUint32(20, size, true);
		cv.setUint32(24, size, true);
		cv.setUint16(28, name.length, true);
		// extra length, comment length, disk number, internal and external
		// attributes are all zero.
		cv.setUint32(42, offset, true); // offset of the local header
		central.set(name, 46);

		locals.push(local, entry.data);
		centrals.push(central);
		offset += local.length + size;
	}

	const centralSize = centrals.reduce((n, c) => n + c.length, 0);
	const end = new Uint8Array(22);
	const ev = new DataView(end.buffer);
	ev.setUint32(0, 0x06054b50, true); // end of central directory signature
	ev.setUint16(8, entries.length, true); // records on this disk
	ev.setUint16(10, entries.length, true); // records in total
	ev.setUint32(12, centralSize, true);
	ev.setUint32(16, offset, true); // where the central directory starts

	const out = new Uint8Array(offset + centralSize + end.length);
	let pos = 0;
	for (const part of [...locals, ...centrals, end]) {
		out.set(part, pos);
		pos += part.length;
	}
	return out;
}
