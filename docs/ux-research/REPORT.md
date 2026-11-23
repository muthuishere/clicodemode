# CLI CodeMode docs UX research

Date: 2026-11-23. Scope: research only, no redesign built. Sources are listed
at the end; every claim about the product is taken from README.md, docs/*.md
and code/SKILL.md on `main` at 9d1ecc1.

## 1. Summary

The current site (gh-pages `index.html`, 18.5 KB, one hand-written page) is a
good landing page and a poor documentation site. It pitches well, its numbers
match the README, it has a dark mode, a skip link and no framework. But
there is nowhere to go after the pitch: every "docs" link leaves the site
for GitHub Markdown, there is no search, no per-topic URL for an agent or a
search engine to land on, and nothing machine-readable (`llms.txt`, Markdown
copies of each page). The surveyed sites aimed at agents (Vercel, Cloudflare,
Mintlify, Bun, Stripe, Claude Code's own docs) all now publish those.

Recommended: keep the landing page hand-rolled, add a docs section built
with Starlight (Astro) from the Markdown already in `docs/`, publish
`llms.txt` and `.md` copies of every page, and deploy both from one GitHub
Actions workflow to GitHub Pages.

## 2. Current site audit

| Area | Current site | What the best sites do | Gap |
|---|---|---|---|
| IA | One page: why, numbers, features, size, quick start, security, links | Landing page, then Get started / Guides / Reference split (Bun, Deno, Supabase, Tailwind) | No second level; SPEC, ARCHITECTURE, MCP, COMPATIBILITY, SKILL only on GitHub |
| Onboarding | 3 steps (go get, register, run) | One copy-paste block that works (Vercel "Deploy an app", Bun `bun run index.tsx`), plus an "agent setup" path (Vercel, Stripe) | No "add to an existing cobra/flag CLI" path, no "try the demo in 30 s" (`go run ./cmd/democli code help` exists in README) and no agent-facing path |
| Navigation | 4 anchor links in a top bar | Persistent sidebar + on-this-page TOC + prev/next | Fine for one page; won't scale past it |
| Search | None | Cmd/Ctrl-K everywhere (Tailwind, Deno, Supabase, Bun "/"); Starlight ships Pagefind static search | Missing |
| Examples | Snippets in sections | Runnable, copyable blocks with filename labels; examples as their own section (htmx, Deno) | No copy buttons, no example gallery (aos snapshot, ctx-optimize chain, streams, workflow) |
| Interactivity | None (one script) | Tabs for alternatives (Claude Code docs: macOS/PowerShell/settings.json tabs) | Could use tabs for "new handler / wrap flag.FlagSet / wrap runner" |
| Agent readability | None | `llms.txt` (Claude Code, Cloudflare, Mintlify), "View as Markdown"/"Copy page" (Bun), front matter with summary (Vercel), docs from the CLI (`stripe docs`, `vercel docs search`) | Missing, and the product's audience is agents. Note the product already has `code docs` and `code skill`, which make a natural parallel to `stripe docs` |
| Mobile | One breakpoint at 560 px, auto-fit grid | Collapsible sidebar, horizontally scrollable tables and code | Wide benchmark tables need overflow wrappers (check on 360 px) |
| SEO | Title and description only | Per-page titles, canonical, Open Graph image, sitemap | No og:*, no canonical, no sitemap, one URL for every topic |
| Accessibility | Skip link, `color-scheme`, prefers-color-scheme, one aria-label | Starlight: landmarks, keyboard search, focus styles, theme toggle | Add a manual theme toggle, check contrast of `<mark>`, table captions |
| Accuracy | Numbers match README | | Hero "One process, not 25" and meta "instead of calling the CLI 25 times": the measured run is **24** aos calls (README). Use 24, or "one process instead of one per call" |

What works and should be kept: the plain, measured tone; the honest
"not a sandbox for hostile code" section; the explicit comparison with
Claude Code's Code Mode; zero JS dependencies on the landing page.

## 3. Survey

| Site | IA | Onboarding | Nav/search | Examples / interactivity | Why it works |
|---|---|---|---|---|---|
| Stripe docs.stripe.com | Use cases first, then "browse by product" | "Start here: skills and plugins", `stripe agent setup`; page readable in terminal via `stripe docs` | Product menu + search | Language-switched code samples | Routes by task, and treats the agent as a reader |
| Vercel vercel.com/docs | Ship / Set up your agent / Build / Secure / Deploy | Three-line terminal block, then an agent prompt ("Fetch .../get-started.md and follow it") | Sitemap, `vercel docs search` from CLI | Page front matter: summary, prerequisites, related | Every page is both human and agent docs |
| Astro Starlight | Getting started, guides, reference, components | `npm create astro@latest -- --template starlight` | Built-in static search, sidebar, TOC | Tabs, asides, code with copy | Docs defaults (search, i18n, a11y, SEO, dark mode) with no custom code |
| Tailwind | Getting started, core concepts, then one page per utility | Install by path (Vite, PostCSS, CLI, CDN) in 3 steps | Cmd-K | Live preview next to code | Reference is the sidebar; finding a utility takes one click |
| Deno docs.deno.com | Runtime / API reference / Examples / Deploy | `deno init`, run, test, add deps | Cmd-K with keyboard nav | Separate Examples section | Clear split between learning and reference |
| Bun bun.sh/docs | Docs / Guides / Reference / Blog; 12 sidebar groups | Installation, then a hello-world server | "/" search | Copy page, View as Markdown, Edit on GitHub on each page | Short pages, Markdown copy for agents |
| Cloudflare Workers | Build with / Integrate with; templates or CLI | Two paths: template or Wrangler | TOC, search | `/workers/llms.txt` | Per-product `llms.txt` |
| Mintlify | Hosted docs platform | | AI search, `llms.txt`, MCP server | | Agent features out of the box, but hosted and paid tiers |
| Supabase | Start / Products / Build / Manage / Reference / Resources | Quickstart per framework | Ctrl-K + Ask AI | Client reference per language | Quickstart matrix meets people where they are |
| htmx | One long docs page + Reference + Examples | Install at the top | Anchor TOC, search | Examples on their own pages | Small library, small site; one page you can read end to end |
| charmbracelet/gum (Go CLI) | README: pitch, tutorial, install, one section per command | Every package manager + `go install` | GitHub TOC | GIF demo per command | Go CLI norm: README is the docs, demos sell it |
| Claude Code docs (Code Mode page) | Feature page: how it works, when to use, limits, requirements, try it, troubleshooting | Numbered "Try Code Mode" steps | `llms.txt` index notice at top of page | OS tabs | Limits and requirements stated before the steps |

Lessons for CLI CodeMode: (1) htmx/gum show a small library does not need a
big site; (2) Bun/Vercel/Cloudflare show agent-readable output is now
table stakes for agent-facing tools; (3) Claude Code's page structure
(how / when / limits / try / troubleshoot) suits each feature page here.

## 4. Prioritized recommendations

P0 (small, do first)
1. Fix the "25" to the measured 24 (or drop the number) in hero and meta description.
2. Add `llms.txt` and `llms-full.txt` (README + SPEC + SKILL concatenated) at the site root.
3. Add og:title/description/image, canonical, sitemap.xml, favicon.
4. Add a "Try it in 30 seconds" block: `git clone ... && go run ./cmd/democli code help`.
5. Add copy buttons to code blocks; wrap tables in `overflow-x:auto`.

P1 (docs section)
6. Publish docs/*.md and code/SKILL.md as site pages with a sidebar and static search.
7. "Copy page / View as Markdown" link on each page (serve the source `.md`).
8. Quick start in tabs: write a handler / wrap a `flag.FlagSet` / wrap a runner (`cmdtree.FromRunner` + `WithJSONResult`).
9. An "For agents" page: how an agent discovers ops (`code capabilities --json`, `code docs`, `code skill`), checks (`code check`) and runs (`code run -`), and the Claude Code Code Mode composition snippet.
10. Examples section from real programs: aos machine snapshot, ctx-optimize chain, YAML stream, workflow export.

P2
11. Generated operation reference for the demo CLI from `code capabilities --json` at build time (proves the self-describing claim).
12. Benchmarks page with method (machine, medians, commands) so numbers can be reproduced.
13. Edit-on-GitHub links; version label (v0.5.x) in the header.

## 5. Proposed IA and page outline

```
/                       Landing (hand-rolled, current page refined)
/docs/                  Overview: what it is, what it is not
  Get started
    quick-start         go get, register, mount `code`, run (tabs)
    try-the-demo        democli in 30 s
    wrap-existing       cmdtree FromRunner / FromFlagSet, WithJSONResult, WithFlagsLast, cobra notes
  Guides
    for-agents          capabilities -> check -> run; SKILL.md; composing with Claude Code Code Mode
    language            the JS subset (SPEC language section): async, Promise.all, imports, pmap
    exec-and-streams    ExecPolicy (deny by default), execStream, .jsonl/.csv/.yaml, kill on cancel
    workflows           workflow/step, run-step, .plan()/.toYAML() for WFNexus
    caching             CODEMODE_CACHE_DIR, capability and compiled caches
    mcp                 optional adapter (docs/MCP.md)
    security            limits, Policy.Allow, not a sandbox
  Reference
    cli                 every `code` subcommand
    go-api              Registry, Operation, Engine, CLI, Policy (link pkg.go.dev)
    spec                docs/SPEC.md (normative)
    architecture        docs/ARCHITECTURE.md
    compatibility       docs/COMPATIBILITY.md (aos batch, WFNexus YAML)
  Benchmarks            tables + method
  Examples              aos snapshot, ctx-optimize chain, streams, workflow
  Roadmap               docs/ROADMAP.md
/llms.txt  /llms-full.txt  /docs/**.md
```

Landing page outline (keep current order, tighten): hero (one sentence + `go get`) ->
"loop in the CLI, not only in the agent" diagram (N processes vs 1) -> measured
tables -> 6 feature cards -> 3-step quick start -> security -> links into /docs.

## 6. Design direction

- Keep the current look: monochrome, system font stack, one accent (`<mark>`), dense tables. It reads as a serious systems library; avoid gradient-hero SaaS styling.
- Monospace for anything typed; every command block copyable; output blocks visually distinct from input.
- One diagram on the landing page: left, agent -> N CLI processes -> N text outputs parsed back; right, agent -> one `code run` -> ops in-process -> one JSON value. Inline SVG, works in both themes.
- Starlight theme customised only via CSS tokens to match the landing page, so the two halves feel like one site.
- Manual light/dark toggle in addition to prefers-color-scheme.

## 7. Platform options (Go repo, GitHub Pages)

| Option | Pros | Cons | Fit |
|---|---|---|---|
| **Starlight (Astro)** | Static output; built-in Pagefind search, sidebar, TOC, dark mode, a11y, SEO, i18n; Markdown in, little config; official GitHub Pages action | Adds Node + package.json to a Go repo (can live in `site/` subdir, only CI needs Node) | Best default |
| VitePress | Very fast, small, built-in local search, Markdown-first | Vue for customisation; fewer docs components (tabs need plugin/custom) | Close second |
| Mintlify | `llms.txt`, AI search, MCP built in; polished | Hosted SaaS, not GitHub Pages; paid tiers for features; content in their format | Overkill for a one-maintainer OSS library |
| Hugo (+ Docsy/Hextra) | Single Go binary, no Node, fits a Go repo culturally | Themes heavier to customise; search needs extra setup | Good if "no Node anywhere" is a hard rule |
| Hand-rolled (extend current) | Zero deps, full control, current page already good | Search, sidebar, per-page SEO, `.md` copies all hand-built and maintained | Keep for landing only |
| README only (gum style) | Nothing to maintain | No search, no agent index, no SEO per topic | Already what docs links point at |

Recommendation: hand-rolled landing at `/` + Starlight docs under `/docs/`,
both built in one Actions workflow. If Node in the repo is unacceptable,
Hugo + Hextra is the fallback.

## 8. Migration plan

1. P0 fixes on the existing gh-pages `index.html` (number, meta, og, llms.txt, sitemap). No tooling change.
2. Add `site/` (Starlight) on a branch; content sourced from `docs/*.md`, `code/SKILL.md`, README sections via symlink or a copy step so `docs/` stays the single source of truth (doc-syncer and SPEC tests keep pointing at it).
3. Move deployment from the hand-pushed gh-pages branch to an Actions Pages workflow (build Starlight, copy landing `index.html` to root, emit `llms.txt` and `.md` copies). Keep gh-pages branch until the new deploy is verified, then retire it.
4. Add a CI check that site build passes and that links resolve.
5. P2 items (generated reference, benchmarks method) after the docs section ships.

Note: the deemwar kernel chapter 14 says UIs go on Cloudflare; a public OSS
docs site on GitHub Pages is the existing choice here and this report keeps it
(open question below).

## 9. Messaging (accurate)

One-liner: "Code Mode for every CLI: a pure-Go library that lets an agent send
one small JavaScript program to `<cli> code run` instead of calling the CLI
once per command."

Supporting lines, each backed by the README:
- No Node.js, V8, Goja or cgo; adds about 1.7 MB to a stripped binary; one dependency (`gopkg.in/yaml.v3`).
- Operations are self-describing (`code capabilities --json`, `code describe`) and programs are checked before they run (`code check`, file:line:col).
- Results are JSON objects in-process, not text to parse back.
- Exec is deny by default behind a host `ExecPolicy`; limits on steps, time, depth and concurrency. Not a sandbox for hostile code.
- MCP is optional.

Claude Code Code Mode distinction (keep on landing and "For agents"):
- Claude Code's Code Mode (experimental, off by default, terminal sessions on recent Claude models) puts the code loop in the **agent**: Claude writes JavaScript that makes several tool calls in one step. Each CLI command it runs (`` $`aos window list --json` ``) is still its own process with text output.
- CLI CodeMode puts the loop in the **CLI**: one `code run`, one process, operations called in-process. It works from any agent or shell that can run a command (Claude Code with or without Code Mode, Codex, Cursor, scripts), and the two compose: a Code Mode snippet can call `<cli> code run -` once.
- Model round trips: one either way. What changes is process count, CPU and bytes returned.

Numbers to use, exactly as measured (medians, Apple Silicon):
- ctx-optimize, 7-call chain: 68 ms / 167 ms CPU / 28,273 bytes as 7 sequential processes (47 ms with cards in parallel) vs **33 ms / 137 ms CPU / 169 bytes** in one `code run`.
- aos, 24 read-only calls: 1,644 ms sequential processes; 217 ms parallel processes (1,493 ms CPU) vs **217 ms (1,012 ms CPU)** in one `code run`. Wall time even with parallel spawning; less CPU.
- Persistent MCP server on the demo CLI: 0.12 ms per call vs 4.68 ms for a new process.

Do not claim: token savings with a real agent (ROADMAP lists that benchmark as "next"), "faster than Node" in general (Node wins the 1M-iteration loop, 25 ms vs 55 ms), sandboxing, or "25 calls".

## 10. Sources

- Current site: https://muthuishere.github.io/clicodemode/ (gh-pages `index.html`, commit ad4c31c)
- Claude Code Code Mode: https://code.claude.com/docs/en/code-mode
- Stripe: https://docs.stripe.com/
- Vercel: https://vercel.com/docs
- Starlight: https://starlight.astro.build/ , https://starlight.astro.build/getting-started/
- Tailwind: https://tailwindcss.com/docs/installation
- Deno: https://docs.deno.com/
- Bun: https://bun.sh/docs
- Cloudflare Workers: https://developers.cloudflare.com/workers/ (and /workers/llms.txt)
- Mintlify: https://mintlify.com/docs
- Supabase: https://supabase.com/docs
- htmx: https://htmx.org/docs/
- gum (Charm Go CLI): https://github.com/charmbracelet/gum
- VitePress: https://vitepress.dev/

Not verified by fetch in this pass (from general knowledge, check before
relying on it): Starlight's search engine is Pagefind; VitePress local search
and Hugo/Hextra details; Tailwind's live-preview behaviour.

## 11. Open questions

1. Is Node acceptable in this Go repo (Starlight/VitePress in `site/`), or must the toolchain stay Go-only (Hugo)?
2. Stay on GitHub Pages at `muthuishere.github.io/clicodemode`, or a custom domain (e.g. on Cloudflare, per kernel ch. 14)?
3. Switch deploys from the hand-pushed gh-pages branch to a GitHub Actions Pages workflow?
4. Should aos and ctx-optimize be named as adopters while their PRs (aos#123, ctx-optimize#20) are unmerged? The current site already shows aos as plain text "until release".
5. Is a real-agent token/turn benchmark planned soon enough to wait for it before the relaunch, or launch with process/CPU/bytes numbers only?
6. Publish a generated operation reference for the demo CLI, or keep reference to the Go API and SPEC?
