// Writes dist/llms.txt (an index) and dist/llms-full.txt (every page's
// Markdown) after `astro build`, following https://llmstxt.org.
import { readFileSync, writeFileSync, readdirSync, statSync } from 'node:fs';
import { dirname, join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const content = join(here, '..', 'src', 'content', 'docs');
const dist = join(here, '..', 'dist');
const URL_BASE = 'https://muthuishere.github.io/clicodemode';

// Order follows the sidebar.
const order = [
	'getting-started', 'how-it-works', 'integrate', 'agents', 'examples/aos', 'examples/ctx-optimize',
	'language', 'operations-api', 'mcp', 'workflows', 'security', 'benchmarks',
	'reference/cli', 'reference/spec', 'reference/architecture', 'reference/compatibility', 'reference/skill', 'faq',
];

function walk(dir) {
	return readdirSync(dir).flatMap((n) => {
		const p = join(dir, n);
		return statSync(p).isDirectory() ? walk(p) : [p];
	});
}
const files = Object.fromEntries(
	walk(content)
		.filter((p) => /\.mdx?$/.test(p))
		.map((p) => [relative(content, p).replace(/\.mdx?$/, '').replace(/\\/g, '/'), p]),
);

function parse(p) {
	const raw = readFileSync(p, 'utf8');
	const end = raw.indexOf('\n---', 4);
	const fm = raw.slice(4, end);
	const get = (k) => {
		const m = fm.match(new RegExp(`^${k}:\\s*(.+)$`, 'm'));
		return m ? m[1].replace(/^["']|["']$/g, '') : '';
	};
	const body = raw
		.slice(end + 4)
		.replace(/^import .+$/gm, '')
		.replace(/^:::\w+(\[.*\])?$|^:::$/gm, '')
		// the language-wrappers note stays on the FAQ page only, not in llms files
		.replace(/^## Which languages are supported\?\n[\s\S]*?(?=^## )/m, '')
		.replace(/\n{3,}/g, '\n\n')
		.trim();
	return { title: get('title'), description: get('description'), body };
}

const missing = order.filter((s) => !files[s]);
if (missing.length) {
	console.error('llms: missing pages', missing);
	process.exit(1);
}
const docs = order.map((s) => ({ slug: s, url: `${URL_BASE}/${s}/`, ...parse(files[s]) }));

const idx = [
	'# CLI CodeMode',
	'',
	'> An embeddable Go library for existing CLIs: register the commands you have, expose a `code` subcommand, and an agent sends one small JavaScript-subset program that calls those operations in one process instead of starting the CLI once per command. Not a new agent; it complements agent-side Code Mode (Claude Code, Codex). Not a sandbox for hostile code.',
	'',
	'Module: `github.com/muthuishere/clicodemode` (pure Go, no Node/V8/cgo). Discover a Code Mode CLI with `<cli> code capabilities --json`, validate with `<cli> code check`, run with `<cli> code run - --input JSON --json`.',
	'',
	'## Docs',
	'',
	...docs.map((d) => `- [${d.title}](${d.url}): ${d.description}`),
	'',
	'## Optional',
	'',
	`- [Full text of every page](${URL_BASE}/llms-full.txt)`,
	'- [Repository](https://github.com/muthuishere/clicodemode)',
	'',
].join('\n');
writeFileSync(join(dist, 'llms.txt'), idx);

const full = docs
	.map((d) => `# ${d.title}\n\nSource: ${d.url}\n\n${d.description ? `> ${d.description}\n\n` : ''}${d.body}\n`)
	.join('\n---\n\n');
writeFileSync(join(dist, 'llms-full.txt'), `# CLI CodeMode: full documentation\n\n${full}`);
console.log(`llms: wrote llms.txt (${docs.length} pages) and llms-full.txt`);
