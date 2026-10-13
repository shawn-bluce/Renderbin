// Every date the UI prints goes through here. `toLocale*String(locale, options)`
// builds a fresh Intl.DateTimeFormat on each call, which is by far the costly
// part of formatting; a list of a few hundred rows did that several times per
// row. One formatter per (locale, options) is built once and reused.
import { intlLocale, t } from '$lib/i18n/index.svelte';

const formatters = new Map<string, Intl.DateTimeFormat>();

function format(date: Date, options: Intl.DateTimeFormatOptions): string {
	if (Number.isNaN(date.getTime())) return '';
	const locale = intlLocale();
	const key = locale + JSON.stringify(options);
	let f = formatters.get(key);
	if (!f) {
		f = new Intl.DateTimeFormat(locale, options);
		formatters.set(key, f);
	}
	return f.format(date);
}

const FULL: Intl.DateTimeFormatOptions = {
	year: 'numeric',
	month: 'numeric',
	day: 'numeric',
	hour: 'numeric',
	minute: 'numeric',
	second: 'numeric'
};

/** Date and time in full, for tooltips and previews. */
export function fullDate(when: string | Date): string {
	return format(new Date(when), FULL);
}

export function formatTime(iso: string): string {
	return format(new Date(iso), { hour: '2-digit', minute: '2-digit' });
}

/**
 * A file's expiry deadline, to the minute -- a link that dies at 18:00 today and
 * one that dies at 06:00 today are not the same thing. The year is left off
 * unless it differs from the current one.
 */
export function formatDeadline(iso: string): string {
	const d = new Date(iso);
	return format(d, {
		year: d.getFullYear() !== new Date().getFullYear() ? 'numeric' : undefined,
		month: 'short',
		day: 'numeric',
		hour: '2-digit',
		minute: '2-digit'
	});
}

/** "Oct 13, 2026" -- a calendar date with no time. */
export function formatDate(iso: string): string {
	return format(new Date(iso), { year: 'numeric', month: 'short', day: 'numeric' });
}

function isSameDay(a: Date, b: Date): boolean {
	return (
		a.getFullYear() === b.getFullYear() &&
		a.getMonth() === b.getMonth() &&
		a.getDate() === b.getDate()
	);
}

/** The list's day heading: Today, Yesterday, or the date (with the year only if it isn't this one). */
export function dayLabel(iso: string): string {
	const d = new Date(iso);
	const now = new Date();
	if (isSameDay(d, now)) return t('date.today');
	const yesterday = new Date(now);
	yesterday.setDate(now.getDate() - 1);
	if (isSameDay(d, yesterday)) return t('date.yesterday');
	return format(d, {
		month: 'short',
		day: 'numeric',
		year: d.getFullYear() !== now.getFullYear() ? 'numeric' : undefined
	});
}
