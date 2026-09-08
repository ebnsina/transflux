import type { Name } from '$lib/components/Icon.svelte';

/**
 * Navigation, grouped by what someone is trying to do rather than by which
 * part of the system serves it.
 */
export type Item = {
	href: '/(app)/dashboard' | '/(app)/assets' | '/(app)/jobs' | '/(app)/workers';
	label: string;
	icon: Name;
};
export type Group = { title: string; items: Item[] };

export const groups: Group[] = [
	{
		title: 'Overview',
		items: [{ href: '/(app)/dashboard', label: 'Dashboard', icon: 'dashboard' }]
	},
	{
		title: 'Library',
		items: [{ href: '/(app)/assets', label: 'Media', icon: 'media' }]
	},
	{
		title: 'Processing',
		items: [{ href: '/(app)/jobs', label: 'Activity', icon: 'activity' }]
	},
	{
		title: 'Infrastructure',
		items: [{ href: '/(app)/workers', label: 'Machines', icon: 'machines' }]
	}
];

/** section returns the top-level item a path belongs to. */
export function section(pathname: string): Item | undefined {
	const items = groups.flatMap((g) => g.items);
	// A route group is part of the id but not of the URL, so compare against
	// the path a browser actually shows.
	const url = (href: string) => href.replace('/(app)', '');
	// Longest match first, so /assets/x picks Media rather than Dashboard.
	return items
		.filter((i) => pathname.startsWith(url(i.href)))
		.sort((a, b) => url(b.href).length - url(a.href).length)[0];
}
