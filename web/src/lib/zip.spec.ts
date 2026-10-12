import { describe, expect, it } from 'vitest';
import { buildZip, crc32 } from './zip';

const enc = new TextEncoder();

function u16(b: Uint8Array, at: number): number {
	return new DataView(b.buffer, b.byteOffset).getUint16(at, true);
}

function u32(b: Uint8Array, at: number): number {
	return new DataView(b.buffer, b.byteOffset).getUint32(at, true);
}

describe('crc32', () => {
	it('matches the standard check values', () => {
		expect(crc32(new Uint8Array())).toBe(0);
		// The canonical CRC-32 check value.
		expect(crc32(enc.encode('123456789'))).toBe(0xcbf43926);
		expect(crc32(enc.encode('The quick brown fox jumps over the lazy dog'))).toBe(0x414fa339);
	});
});

describe('buildZip', () => {
	it('writes an empty archive as a bare end-of-directory record', () => {
		const z = buildZip([]);
		expect(z.length).toBe(22);
		expect(u32(z, 0)).toBe(0x06054b50);
	});

	it('lays out local headers, data, central directory and end record', () => {
		const a = enc.encode('<p>hi</p>');
		const b = enc.encode('body{}');
		const z = buildZip([
			{ path: 'index.html', data: a },
			{ path: '目录/a.css', data: b }
		]);

		// First local header, stored, UTF-8 flag set, sizes and CRC filled in.
		expect(u32(z, 0)).toBe(0x04034b50);
		expect(u16(z, 6) & 0x0800).toBe(0x0800);
		expect(u16(z, 8)).toBe(0);
		expect(u32(z, 14)).toBe(crc32(a));
		expect(u32(z, 18)).toBe(a.length);
		expect(u32(z, 22)).toBe(a.length);
		expect(new TextDecoder().decode(z.subarray(30, 30 + u16(z, 26)))).toBe('index.html');
		expect(new TextDecoder().decode(z.subarray(40, 40 + a.length))).toBe('<p>hi</p>');

		// The end record points at a central directory holding both entries.
		const end = z.length - 22;
		expect(u32(z, end)).toBe(0x06054b50);
		expect(u16(z, end + 10)).toBe(2);
		const cdStart = u32(z, end + 16);
		const cdSize = u32(z, end + 12);
		expect(cdStart + cdSize).toBe(end);
		expect(u32(z, cdStart)).toBe(0x02014b50);

		// The second central record names the non-ASCII path in UTF-8 and
		// points at its local header.
		const second = cdStart + 46 + u16(z, cdStart + 28);
		expect(u32(z, second)).toBe(0x02014b50);
		const nameLen = u16(z, second + 28);
		expect(new TextDecoder().decode(z.subarray(second + 46, second + 46 + nameLen))).toBe(
			'目录/a.css'
		);
		const localAt = u32(z, second + 42);
		expect(u32(z, localAt)).toBe(0x04034b50);
		expect(u32(z, localAt + 14)).toBe(crc32(b));
	});

	it('is deterministic', () => {
		const entries = [{ path: 'index.html', data: enc.encode('x') }];
		expect(buildZip(entries)).toEqual(buildZip(entries));
	});
});
