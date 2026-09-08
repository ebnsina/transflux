/**
 * The trailing breadcrumb for a detail page.
 *
 * Only the page itself knows what a record is called — a job id means nothing
 * to a reader — so a page sets its own label and the layout builds the rest
 * from the route.
 */
class Trail {
	label = $state<string | null>(null);
}

const trail = new Trail();

export function setTrail(label: string | null) {
	trail.label = label;
}

export function currentTrail(): string | null {
	return trail.label;
}
