// Emits AI-friendly plain-text views of the documentation at build time:
//   /llms.txt        an index with one line per page (title, URL, summary)
//   /llms-full.txt   every page's Markdown, concatenated, with source URLs
//   /<route>.md      the raw Markdown of each page next to its HTML
// The canonical HTML pages link to their Markdown twin via <link rel="alternate">
// so agents that land on a page can find the plain version.
const fs = require('fs');
const path = require('path');

function frontmatter(src) {
  const m = src.match(/^---\n([\s\S]*?)\n---\n?/);
  if (!m) return {meta: {}, body: src};
  const meta = {};
  for (const line of m[1].split('\n')) {
    const i = line.indexOf(':');
    if (i > 0) meta[line.slice(0, i).trim()] = line.slice(i + 1).trim().replace(/^["']|["']$/g, '');
  }
  return {meta, body: src.slice(m[0].length)};
}

function walk(dir) {
  const out = [];
  for (const e of fs.readdirSync(dir, {withFileTypes: true})) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) out.push(...walk(p));
    else if (e.name.endsWith('.md') || e.name.endsWith('.mdx')) out.push(p);
  }
  return out.sort();
}

function routeFor(docsDir, file, meta) {
  if (meta.slug) return meta.slug;
  const rel = path.relative(docsDir, file).replace(/\.mdx?$/, '');
  return '/' + rel.replace(/\/index$/, '').replace(/^index$/, '');
}

module.exports = function llmsTxtPlugin(context) {
  const docsDir = path.join(context.siteDir, 'docs');
  const site = context.siteConfig.url.replace(/\/$/, '');
  return {
    name: 'nomctl-llms-txt',
    async postBuild({outDir}) {
      const pages = [];
      for (const file of walk(docsDir)) {
        const {meta, body} = frontmatter(fs.readFileSync(file, 'utf8'));
        const route = routeFor(docsDir, file, meta) || '/overview';
        const title = meta.title || path.basename(file, path.extname(file));
        const summary = meta.description || '';
        const md = `# ${title}\n\nSource: ${site}${route}\n\n${body.trim()}\n`;
        const target = path.join(outDir, route.replace(/^\//, '') + '.md');
        fs.mkdirSync(path.dirname(target), {recursive: true});
        fs.writeFileSync(target, md);
        pages.push({title, route, summary, md});
      }
      const index = [
        '# nomctl',
        '',
        '> nomctl is a single static binary for deploying, backing up, monitoring and alerting on Zenon Network (NoM) nodes on Debian/Ubuntu. A Go port of the hypercore-one deployment scripts with a TUI for humans and subcommands for scripts.',
        '',
        `Site: ${site}. Every page is also available as Markdown at the same path with a .md suffix, and ${site}/llms-full.txt contains all pages in one file.`,
        '',
        '## Documentation',
        '',
        ...pages.map((p) => `- [${p.title}](${site}${p.route}.md)${p.summary ? ': ' + p.summary : ''}`),
        '',
        '## Source',
        '',
        '- [GitHub repository](https://github.com/0x3639/nomctl)',
        '- [Releases](https://github.com/0x3639/nomctl/releases)',
        '',
      ].join('\n');
      fs.writeFileSync(path.join(outDir, 'llms.txt'), index);
      fs.writeFileSync(path.join(outDir, 'llms-full.txt'), pages.map((p) => p.md).join('\n---\n\n'));
    },
  };
};
