// Checks every internal href/src in dist/*.html resolves to a built file,
// and that #fragments exist on the target page.
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const dist = join(dirname(fileURLToPath(import.meta.url)), '..', 'dist');
const BASE = '/clicodemode';
const walk = (d) => readdirSync(d).flatMap((n) => (statSync(join(d, n)).isDirectory() ? walk(join(d, n)) : [join(d, n)]));
const html = walk(dist).filter((p) => p.endsWith('.html'));
const ids = new Map();
const idsOf = (file) => {
	if (!ids.has(file)) {
		const s = readFileSync(file, 'utf8');
		ids.set(file, new Set([...s.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1])));
	}
	return ids.get(file);
};
function target(urlPath) {
	if (!urlPath.startsWith(BASE + '/') && urlPath !== BASE) return null;
	let p = decodeURIComponent(urlPath.slice(BASE.length)) || '/';
	const f = join(dist, p);
	if (p.endsWith('/')) return join(f, 'index.html');
	if (existsSync(f) && statSync(f).isFile()) return f;
	return join(f, 'index.html');
}
let bad = 0, checked = 0;
for (const file of html) {
	const s = readFileSync(file, 'utf8');
	const pageUrl = '/' + file.slice(dist.length + 1).replace(/index\.html$/, '');
	for (const m of s.matchAll(/\s(?:href|src)="([^"]+)"/g)) {
		let u = m[1].replace(/&amp;/g, '&');
		if (/^(https?:|mailto:|data:|javascript:)/.test(u) || u.startsWith('//')) continue;
		const [p, frag] = u.split('#');
		let t;
		if (p === '') t = file;
		else if (p.startsWith('/')) t = target(p.split('?')[0]);
		else t = target(new URL(p, 'http://x' + BASE + pageUrl).pathname);
		checked++;
		if (!t || !existsSync(t)) { console.error(`BROKEN ${pageUrl}: ${u}`); bad++; continue; }
		if (frag && t.endsWith('.html') && !idsOf(t).has(decodeURIComponent(frag))) {
			console.error(`BROKEN ANCHOR ${pageUrl}: ${u}`); bad++;
		}
	}
}
console.log(`links: ${checked} internal links checked in ${html.length} pages, ${bad} broken`);
process.exit(bad ? 1 : 0);
