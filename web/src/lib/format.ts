export function bytes(n?: number): string {
	if (!n) return '—';
	const units = ['B', 'KB', 'MB', 'GB', 'TB'];
	let value = n;
	let unit = 0;
	while (value >= 1024 && unit < units.length - 1) {
		value /= 1024;
		unit++;
	}
	return `${value < 10 && unit > 0 ? value.toFixed(1) : Math.round(value)} ${units[unit]}`;
}

export function duration(ms?: number): string {
	if (!ms) return '—';
	const total = Math.round(ms / 1000);
	const h = Math.floor(total / 3600);
	const m = Math.floor((total % 3600) / 60);
	const s = total % 60;
	if (h > 0) return `${h}h ${m}m`;
	if (m > 0) return `${m}m ${s}s`;
	return `${s}s`;
}

export function seconds(v?: number): string {
	if (v === undefined || v === null) return '—';
	return v < 10 ? `${v.toFixed(2)}s` : `${Math.round(v)}s`;
}

export function ago(iso?: string): string {
	if (!iso) return '—';
	const delta = (Date.now() - new Date(iso).getTime()) / 1000;
	if (delta < 60) return `${Math.max(0, Math.round(delta))}s ago`;
	if (delta < 3600) return `${Math.round(delta / 60)}m ago`;
	if (delta < 86400) return `${Math.round(delta / 3600)}h ago`;
	return `${Math.round(delta / 86400)}d ago`;
}

export function fps(num?: number, den?: number): string {
	if (!num || !den) return '—';
	const value = num / den;
	return Number.isInteger(value) ? `${value}` : value.toFixed(2);
}

/** short renders an id as its leading segment, which is enough to tell rows apart. */
export function short(id: string): string {
	return id.split('-')[0];
}
