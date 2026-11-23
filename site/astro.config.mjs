// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

const site = 'https://muthuishere.github.io';
const base = '/clicodemode';
const ogImage = `${site}${base}/og.png`;

export default defineConfig({
	site,
	base,
	trailingSlash: 'always',
	integrations: [
		starlight({
			title: 'CLI CodeMode',
			description:
				'An embeddable Go library for existing CLIs: register the commands you have, expose `code`, and agents send one program instead of many processes.',
			logo: { src: './src/assets/mark.svg', replacesTitle: false },
			favicon: '/favicon.svg',
			social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/muthuishere/clicodemode' }],
			editLink: { baseUrl: 'https://github.com/muthuishere/clicodemode/edit/main/site/src/content/docs/' },
			lastUpdated: false,
			customCss: ['./src/styles/theme.css'],
			expressiveCode: {
				themes: ['github-dark-default', 'github-light-default'],
				styleOverrides: {
					borderRadius: '8px',
					borderColor: 'var(--cc-line)',
					codeFontFamily: 'var(--cc-mono)',
					codeFontSize: '0.84rem',
					frames: { shadowColor: 'transparent', editorActiveTabIndicatorTopColor: 'var(--cc-accent)' },
				},
			},
			head: [
				{ tag: 'meta', attrs: { property: 'og:image', content: ogImage } },
				{ tag: 'meta', attrs: { name: 'twitter:card', content: 'summary_large_image' } },
				{ tag: 'meta', attrs: { name: 'twitter:image', content: ogImage } },
				{ tag: 'link', attrs: { rel: 'alternate', type: 'text/plain', title: 'llms.txt', href: `${base}/llms.txt` } },
			],
			sidebar: [
				{ label: 'Get started', link: '/getting-started/' },
				{ label: 'How it works', link: '/how-it-works/' },
				{ label: 'Integrate your CLI', link: '/integrate/' },
				{ label: 'Use from an agent or skill', link: '/agents/' },
				{
					label: 'Real examples',
					items: [
						{ label: 'aos', link: '/examples/aos/', badge: { text: 'open PR', variant: 'note' } },
						{ label: 'ctx-optimize', link: '/examples/ctx-optimize/', badge: { text: 'open PR', variant: 'note' } },
					],
				},
				{ label: 'JS subset and limitations', link: '/language/' },
				{ label: 'Operations API', link: '/operations-api/' },
				{ label: 'MCP adapter (optional)', link: '/mcp/' },
				{ label: 'Workflows and WFNexus', link: '/workflows/' },
				{ label: 'Security and policy', link: '/security/' },
				{ label: 'Benchmarks and methodology', link: '/benchmarks/' },
				{
					label: 'Reference',
					items: [
						{ label: 'CLI reference', link: '/reference/cli/' },
						{ label: 'Specification', link: '/reference/spec/' },
						{ label: 'Architecture', link: '/reference/architecture/' },
						{ label: 'Compatibility', link: '/reference/compatibility/' },
						{ label: 'Agent skill (SKILL.md)', link: '/reference/skill/' },
					],
				},
				{ label: 'FAQ', link: '/faq/' },
			],
		}),
	],
});
