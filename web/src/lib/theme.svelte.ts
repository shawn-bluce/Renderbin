// Light/dark theme. Dependency-free, same shape as the i18n runtime: one
// module-scoped $state rune. app.html's inline script has already put the right
// value on <html data-theme> before first paint; this only mirrors it into
// reactive state and handles the toggle.
const STORAGE_KEY = 'rb_theme';
const BAR_COLOR = { light: '#f9f5d7', dark: '#1d2021' } as const;

export type Theme = 'light' | 'dark';

let theme = $state<Theme>('dark');

function apply(next: Theme) {
	theme = next;
	document.documentElement.dataset.theme = next;
	document.querySelector('meta[name=theme-color]')?.setAttribute('content', BAR_COLOR[next]);
}

// Storage can be disabled outright (some private-browsing modes throw on access);
// the theme must still work, just without being remembered.
function stored(): Theme | null {
	try {
		const v = localStorage.getItem(STORAGE_KEY);
		return v === 'light' || v === 'dark' ? v : null;
	} catch {
		return null;
	}
}

// Called once from the root layout. Until the user picks a theme themselves the
// OS setting stays in charge, including when it changes while the app is open.
export function initTheme() {
	const media = matchMedia('(prefers-color-scheme: light)');
	apply(stored() ?? (media.matches ? 'light' : 'dark'));
	media.addEventListener('change', (e) => {
		if (!stored()) apply(e.matches ? 'light' : 'dark');
	});
}

export function getTheme(): Theme {
	return theme;
}

export function toggleTheme() {
	const next: Theme = theme === 'dark' ? 'light' : 'dark';
	try {
		localStorage.setItem(STORAGE_KEY, next);
	} catch {
		/* not remembered */
	}
	apply(next);
}
