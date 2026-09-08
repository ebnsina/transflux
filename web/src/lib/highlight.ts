// One highlighter for the whole site. Only the languages actually shown are
// registered: the root entry of @tanstack/highlight pulls in every language it
// ships, which is a lot of parser for two code samples.
import { createHighlighter } from '@tanstack/highlight/core';
import { json } from '@tanstack/highlight/languages/json';
import { shell } from '@tanstack/highlight/languages/shell';

const highlighter = createHighlighter({ languages: [json, shell] });

// Returns the token markup only, without the <pre><code> wrapper, so the
// caller keeps control of the element it renders into. The highlighter escapes
// everything it does not tokenise, so the result is safe to inject.
export function tokens(code: string, lang: 'json' | 'shell'): string {
	const html = highlighter.highlightToHtml(code, { lang });
	const open = html.indexOf('<code>');
	const close = html.lastIndexOf('</code>');
	return open === -1 || close === -1 ? html : html.slice(open + 6, close);
}
