<script lang="ts">
	import { page } from '$app/state';
	import { resolve } from '$app/paths';

	// Every failure a person can land on, in words that say what to do next
	// rather than what went wrong internally.
	const messages: Record<number, { title: string; body: string }> = {
		404: {
			title: 'This page does not exist',
			body: 'The link may be out of date, or whatever was here has been removed.'
		},
		403: {
			title: 'You do not have access to this',
			body: 'Your access key does not cover this part of the dashboard.'
		},
		401: {
			title: 'Your session has ended',
			body: 'Sign in again to carry on.'
		},
		500: {
			title: 'Something went wrong on our side',
			body: 'This is not your fault. Try again in a moment.'
		},
		503: {
			title: 'The service is temporarily unavailable',
			body: 'It should be back shortly. Try again in a moment.'
		}
	};

	const shown = $derived(
		messages[page.status] ?? {
			title: 'Something went wrong',
			body: 'Try again, or head back to the overview.'
		}
	);
</script>

<section class="failure">
	<p class="code">{page.status}</p>
	<h1>{shown.title}</h1>
	<p class="muted">{shown.body}</p>
	<a class="back" href={resolve('/')}>Back to overview</a>
</section>

<style>
	.failure {
		max-width: 480px;
		margin: 96px auto;
		text-align: center;
	}
	.code {
		font-family: var(--font-mono);
		font-size: 13px;
		color: var(--muted);
		margin: 0 0 10px;
		letter-spacing: 0.08em;
	}
	h1 {
		margin-bottom: 10px;
	}
	.back {
		display: inline-block;
		margin-top: 22px;
		border: 1px solid var(--line);
		border-radius: var(--radius-sm);
		padding: 9px 18px;
		color: var(--text);
	}
	.back:hover {
		border-color: var(--accent);
		background: var(--accent-soft);
		text-decoration: none;
	}
</style>
