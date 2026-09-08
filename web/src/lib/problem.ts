import { ApiError } from '$lib/api';
import { problem } from '$lib/words';

/**
 * Turns anything thrown into a sentence worth showing.
 *
 * A network failure, a thrown string and an API error all reach a person the
 * same way, so they are all translated here rather than at each call site.
 */
export function describe(error: unknown): string {
	if (error instanceof ApiError) return problem(error.code);
	if (error instanceof TypeError) {
		// fetch rejects with a TypeError when it cannot reach the server at all.
		return "We couldn't reach the service. Check your connection and try again.";
	}
	return problem(undefined);
}
