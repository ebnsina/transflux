import type { Name } from '$lib/components/Icon.svelte';

/**
 * Navigation, grouped by what someone is trying to do rather than by which
 * part of the system serves it.
 */
export type Item = { href: '/' | '/assets' | '/jobs' | '/workers'; label: string; icon: Name };
export type Group = { title: string; items: Item[] };

export const groups: Group[] = [
	{
		title: 'Overview',
		items: [{ href: '/', label: 'Dashboard', icon: 'dashboard' }]
	},
	{
		title: 'Library',
		items: [{ href: '/assets', label: 'Media', icon: 'media' }]
	},
	{
		title: 'Processing',
		items: [{ href: '/jobs', label: 'Activity', icon: 'activity' }]
	},
	{
		title: 'Infrastructure',
		items: [{ href: '/workers', label: 'Machines', icon: 'machines' }]
	}
];

/** section returns the top-level item a path belongs to. */
export function section(pathname: string): Item | undefined {
	const items = groups.flatMap((g) => g.items);
	// Longest match first, so /assets/x picks Media rather than Dashboard.
	return items
		.filter((i) => (i.href === '/' ? pathname === '/' : pathname.startsWith(i.href)))
		.sort((a, b) => b.href.length - a.href.length)[0];
}
