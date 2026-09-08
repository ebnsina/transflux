/**
 * Navigation, grouped by what someone is trying to do rather than by which
 * part of the system serves it.
 */
export type Item = { href: '/' | '/assets' | '/jobs' | '/workers'; label: string; hint: string };
export type Group = { title: string; items: Item[] };

export const groups: Group[] = [
	{
		title: 'Overview',
		items: [{ href: '/', label: 'Dashboard', hint: 'What is happening right now' }]
	},
	{
		title: 'Library',
		items: [{ href: '/assets', label: 'Media', hint: 'Everything you have added' }]
	},
	{
		title: 'Processing',
		items: [{ href: '/jobs', label: 'Activity', hint: 'Work in progress and finished' }]
	},
	{
		title: 'Infrastructure',
		items: [{ href: '/workers', label: 'Machines', hint: 'What is available to do the work' }]
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
