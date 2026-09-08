/**
 * Theme preference.
 *
 * Three states, not two: "system" follows the operating system, and light or
 * dark is an explicit choice that wins in either direction. Collapsing this to
 * a boolean loses the ability to follow the system at all.
 */
export type Theme = 'system' | 'light' | 'dark';

const STORAGE = 'transflux.theme';

export function stored(): Theme {
	if (typeof localStorage === 'undefined') return 'system';
	const value = localStorage.getItem(STORAGE);
	return value === 'light' || value === 'dark' ? value : 'system';
}

export function apply(theme: Theme) {
	if (typeof document === 'undefined') return;
	if (theme === 'system') {
		// Removing the attribute hands the decision back to the media query
		// rather than freezing whichever mode happened to be active.
		document.documentElement.removeAttribute('data-theme');
		localStorage.removeItem(STORAGE);
		return;
	}
	document.documentElement.setAttribute('data-theme', theme);
	localStorage.setItem(STORAGE, theme);
}

export function next(theme: Theme): Theme {
	return theme === 'system' ? 'light' : theme === 'light' ? 'dark' : 'system';
}

export function label(theme: Theme): string {
	return theme === 'system' ? 'Auto' : theme === 'light' ? 'Light' : 'Dark';
}
