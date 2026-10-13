// Dependency-free i18n runtime built on a single module-scoped $state rune.
// Every translated string in the app flows through `t()`, which reads `locale`;
// because that read happens inside components' templates and $derived blocks,
// switching the locale re-renders all translated text with no page reload and
// no store library — consistent with this project's "runes only" convention.
import { dictionaries, type Locale, type MessageKey } from './messages';

const STORAGE_KEY = 'rb_locale';

let locale = $state<Locale>('en');

function isLocale(v: unknown): v is Locale {
	return v === 'en' || v === 'zh';
}

function intlFor(l: Locale): string {
	return l === 'zh' ? 'zh-CN' : 'en-US';
}

function applyDocumentLang(l: Locale) {
	document.documentElement.lang = intlFor(l);
}

// Storage can be disabled outright (some private-browsing modes throw on access);
// the app must still start, just without remembering the choice.
function readStored(): string | null {
	try {
		return localStorage.getItem(STORAGE_KEY);
	} catch {
		return null;
	}
}

// Called once at startup from the root layout. Restores a persisted choice, or
// falls back to the browser's preferred language (Chinese → zh, else English).
export function initI18n() {
	let next: Locale = 'en';
	const stored = readStored();
	if (isLocale(stored)) {
		next = stored;
	} else if (navigator.language.toLowerCase().startsWith('zh')) {
		next = 'zh';
	}
	locale = next;
	applyDocumentLang(next);
}

export function setLocale(l: Locale) {
	locale = l;
	try {
		localStorage.setItem(STORAGE_KEY, l);
	} catch {
		/* storage disabled: the choice just won't survive a reload */
	}
	applyDocumentLang(l);
}

// Reactive getter — reading this in a template tracks locale changes.
export function getLocale(): Locale {
	return locale;
}

// Locale tag for Intl / toLocale* calls (dates, sorting).
export function intlLocale(): string {
	return intlFor(locale);
}

// Translate a key, filling {name} placeholders. Falls back to English, then to
// the raw key, so a missing translation degrades gracefully rather than crashing.
export function t(key: MessageKey, params?: Record<string, string | number>): string {
	let msg = dictionaries[locale][key] ?? dictionaries.en[key] ?? key;
	if (params) {
		for (const [k, v] of Object.entries(params)) {
			msg = msg.replaceAll(`{${k}}`, String(v));
		}
	}
	return msg;
}
