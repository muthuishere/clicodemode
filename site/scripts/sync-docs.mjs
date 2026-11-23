// Copies the repo's Markdown (docs/*.md, code/SKILL.md) into the Starlight
// content folder with frontmatter. docs/ stays the source of truth; the
// copies are generated and gitignored.
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const repo = join(here, '..', '..');
const out = join(here, '..', 'src', 'content', 'docs');
const BASE = '/clicodemode';
const GH = 'https://github.com/muthuishere/clicodemode';

// source (repo-relative) -> page slug, description
const pages = [
	['docs/SPEC.md', 'reference/spec', 'Normative contract for codemode/v0: CLI, capabilities, operations, language, limits, exec, workflows, MCP.'],
	['docs/ARCHITECTURE.md', 'reference/architecture', 'Packages, operations, caches and adapters, and how they fit together.'],
	['docs/COMPATIBILITY.md', 'reference/compatibility', 'How Code Mode coexists with AOS batch YAML and WFNexus workflow YAML without changing them.'],
	['docs/MCP.md', 'mcp', 'An optional stdio MCP server for agents that only speak the Model Context Protocol.'],
	['code/SKILL.md', 'reference/skill', 'The generic agent skill for driving any Code Mode CLI.'],
];
const slugOf = Object.fromEntries(pages.map(([src, slug]) => [src, slug]));

function rewriteLink(target, fromSrc) {
	if (/^(https?:|mailto:|#)/.test(target)) return target;
	const [path, hash = ''] = target.split('#');
	const resolved = join(dirname(fromSrc), path).replace(/\\/g, '/');
	const anchor = hash ? `#${hash}` : '';
	if (slugOf[resolved]) return `${BASE}/${slugOf[resolved]}/${anchor}`;
	if (resolved === 'README.md') return `${BASE}/getting-started/${anchor}`;
	if (resolved === 'docs/ROADMAP.md') return `${GH}/blob/main/docs/ROADMAP.md${anchor}`;
	return `${GH}/blob/main/${resolved}${anchor}`;
}

for (const [src, slug, description] of pages) {
	let text = readFileSync(join(repo, src), 'utf8');
	let title = '';
	// drop existing frontmatter (SKILL.md has one)
	if (text.startsWith('---\n')) {
		const end = text.indexOf('\n---', 4);
		text = text.slice(end + 4).replace(/^\n+/, '');
	}
	const h1 = text.match(/^# (.+)\n/m);
	if (h1) {
		title = h1[1].trim();
		text = text.replace(h1[0], '');
	}
	text = text.replace(/\]\(([^)\s]+)\)/g, (_, t) => `](${rewriteLink(t, src)})`);
	const fm = [
		'---',
		`title: ${JSON.stringify(title)}`,
		`description: ${JSON.stringify(description)}`,
		`editUrl: ${JSON.stringify(`${GH}/edit/main/${src}`)}`,
		'---',
		'',
		`:::note[Source]\nThis page is generated from [\`${src}\`](${GH}/blob/main/${src}) in the repository.\n:::`,
		'',
	].join('\n');
	const dest = join(out, `${slug}.md`);
	mkdirSync(dirname(dest), { recursive: true });
	writeFileSync(dest, fm + text);
	console.log(`sync ${src} -> src/content/docs/${slug}.md`);
}
