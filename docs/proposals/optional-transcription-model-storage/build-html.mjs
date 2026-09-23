#!/usr/bin/env node
// Run from any directory: node docs/proposals/optional-transcription-model-storage/build-html.mjs
// Uses the workspace's marked and playwright packages. Mermaid is fetched only
// when its SVG cache needs rebuilding; the resulting index.html is fully offline.
import { readFile, writeFile, mkdir } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { marked, Renderer } from 'marked';

const dir = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(dir, '../../..');
const assetDir = path.join(dir, 'html');
await mkdir(assetDir, { recursive: true });
const commit = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: repo, encoding: 'utf8' }).trim();
const repoURL = 'https://github.com/codemyriad/gocassini/blob/' + commit + '/';
const documents = [
  { id: 'proposal', file: 'README.md', label: 'Shaping proposal', short: 'Proposal', subtitle: 'Requirements, alternatives, and the recommended shape', title: 'Optional transcription and persistent models' },
  { id: 'frame', file: 'frame.md', label: 'Problem frame', short: 'Frame', subtitle: 'The request, constraints, and intended outcome', title: 'The problem worth solving' },
  { id: 'research', file: 'spike-current-system.md', label: 'Research & evidence', short: 'Research', subtitle: 'Repository findings, source material, and release gates', title: 'What the research established' },
  { id: 'slices', file: 'slices.md', label: 'Implementation slices', short: 'Slices', subtitle: 'Four increments with visible demos and acceptance criteria', title: 'From shape to working software' },
  { id: 'implementation', file: 'implementation.md', label: 'Implementation & operation', short: 'Operation', subtitle: 'Configuration, air-gapped provisioning, and validation', title: 'Use optional transcription and persistent models' },
  { id: 'original-notes', file: 'nextcloud-storage-research.txt', label: 'Original storage notes', short: 'Original notes', subtitle: 'The supplied research, preserved in full', title: 'The starting research' },
];
const byFile = new Map(documents.map(doc => [doc.file, doc]));
const esc = value => String(value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
const slug = value => value.replace(/<[^>]*>/g, '').replace(/[`*_]/g, '').toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu, '').trim().replace(/\s/g, '-');

async function diagramSVG(source) {
  const digest = createHash('sha256').update('mermaid-11.4.1-v2\n' + source).digest('hex');
  const cache = path.join(assetDir, 'breadboard.svg');
  const saved = await readFile(cache, 'utf8').catch(() => '');
  if (saved.startsWith('<!-- source-sha256: ' + digest + ' -->')) return saved;
  const { chromium } = await import('playwright');
  let bundle;
  if (process.env.CASSINI_MERMAID_BUNDLE) {
    bundle = await readFile(process.env.CASSINI_MERMAID_BUNDLE, 'utf8');
  } else {
    const response = await fetch('https://cdn.jsdelivr.net/npm/mermaid@11.4.1/dist/mermaid.min.js');
    if (!response.ok) throw new Error('Cannot fetch Mermaid: HTTP ' + response.status);
    bundle = await response.text();
  }
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({ viewport: { width: 1600, height: 1100 } });
    await page.setContent('<!doctype html><html><body></body></html>');
    await page.addScriptTag({ content: bundle });
    const svg = await page.evaluate(async text => {
      mermaid.initialize({
        startOnLoad: false, theme: 'base', securityLevel: 'strict',
        themeVariables: {
          fontFamily: 'Arial, sans-serif', fontSize: '15px', primaryColor: '#eef5f1',
          primaryTextColor: '#183c30', primaryBorderColor: '#8ba798', lineColor: '#668477',
          secondaryColor: '#f4f0e8', tertiaryColor: '#ffffff', clusterBkg: '#f8faf8',
          clusterBorder: '#cddad2', edgeLabelBackground: '#ffffff',
        },
        flowchart: { htmlLabels: false, curve: 'basis', nodeSpacing: 35, rankSpacing: 45 },
      });
      return (await mermaid.render('shaping-breadboard', text)).svg;
    }, source.replace(/^flowchart LR\b/, 'flowchart TB'));
    const output = '<!-- source-sha256: ' + digest + ' -->\n' + svg;
    await writeFile(cache, output);
    return output;
  } finally { await browser.close(); }
}

for (const doc of documents) {
  doc.source = await readFile(path.join(dir, doc.file), 'utf8');
  doc.markdown = doc.source.replace(/^---\n[\s\S]*?\n---\n/, '');
  doc.headings = [];
  doc.diagram = '';
  for (const match of doc.markdown.matchAll(/```mermaid\n([\s\S]*?)```/g)) {
    doc.diagram = await diagramSVG(match[1].trim());
  }
  const used = new Map();
  const renderer = new Renderer();
  renderer.heading = function (token) {
    if (token.depth === 1) return '';
    const label = this.parser.parseInline(token.tokens);
    const base = slug(token.text);
    const count = used.get(base) ?? 0;
    used.set(base, count + 1);
    const id = doc.id + '--' + base + (count ? '-' + count : '');
    doc.headings.push({ id, label: token.text, depth: token.depth });
    return '<h' + token.depth + ' id="' + esc(id) + '">' + label +
      '<a class="heading-anchor" href="#' + esc(id) + '" aria-label="Link to this section">#</a></h' + token.depth + '>\n';
  };
  renderer.link = function (token) {
    let href = token.href;
    if (href.startsWith('#')) href = '#' + doc.id + '--' + href.slice(1);
    else if (!/^[a-z][a-z\d+.-]*:/i.test(href)) {
      const [file, anchor] = href.split('#');
      const target = byFile.get(file.replace(/^\.\//, ''));
      if (target) href = '#' + target.id + (anchor ? '--' + anchor : '');
      else {
        const rel = path.relative(repo, path.resolve(dir, file)).split(path.sep).join('/');
        href = repoURL + rel + (anchor ? '#' + anchor : '');
      }
    }
    const result = Renderer.prototype.link.call(this, { ...token, href });
    return /^https?:/.test(href) ? result.replace('<a ', '<a target="_blank" rel="noopener noreferrer" ') : result;
  };
  renderer.table = function (token) {
    const matrix = token.header.map(c => c.text).join('|').endsWith('A|B|C');
    let html = Renderer.prototype.table.call(this, token);
    html = html.replaceAll('✅', '<span class="fit-mark pass" aria-label="Pass" title="Pass">✓</span>')
      .replaceAll('❌', '<span class="fit-mark fail" aria-label="Fail" title="Fail">×</span>');
    for (const status of ['Must-have', 'Core goal', 'Leaning yes', 'Out']) {
      html = html.replaceAll('>' + status + '</td>', '><span class="status-chip">' + status + '</span></td>');
    }
    return '<div class="table-scroll' + (matrix ? ' fit-table' : '') + '" tabindex="0" role="region" aria-label="' +
      (matrix ? 'Requirement fit check' : 'Document table') + ', scroll horizontally when needed">' + html + '</div>\n';
  };
  renderer.code = function (token) {
    if (token.lang === 'mermaid') {
      return '<figure class="breadboard"><div class="figure-toolbar"><span>System wiring</span>' +
        '<button class="text-button" type="button" data-expand-diagram>Expand diagram ↗</button></div>' +
        '<div class="diagram-canvas" tabindex="0" role="region" aria-label="System wiring diagram, scroll horizontally">' + doc.diagram + '</div>' +
        '<figcaption>Solid lines: actions and writes. Dashed lines: returned data. Scroll to explore.</figcaption></figure>' +
        '<details class="diagram-source"><summary>View Mermaid source</summary><pre><code>' + esc(token.text) + '</code></pre></details>';
    }
    return Renderer.prototype.code.call(this, token);
  };
  doc.body = marked.parse(doc.markdown, { renderer, gfm: true, async: false });
  doc.minutes = Math.max(1, Math.ceil(doc.markdown.split(/\s+/).length / 230));
}

const css = await readFile(path.join(assetDir, 'style.css'), 'utf8');
const js = await readFile(path.join(assetDir, 'reader.js'), 'utf8');
const nav = documents.map((doc, i) => '<a class="doc-link" href="#' + doc.id + '" data-document="' + doc.id + '">' +
  '<span class="doc-number">0' + (i + 1) + '</span><span>' + esc(doc.label) + '</span><span class="nav-arrow" aria-hidden="true">↗</span></a>').join('\n');
const articles = documents.map((doc, i) => '<article class="document doc-' + doc.id + '" id="' + doc.id + '" data-label="' + esc(doc.label) + '">' +
  '<header class="document-header"><div class="eyebrow">' + esc(doc.label) + '<span>0' + (i + 1) + ' / ' + documents.length + '</span></div>' +
  '<h1>' + esc(doc.title) + '</h1><p class="document-subtitle">' + esc(doc.subtitle) + '</p>' +
  '<div class="document-meta"><span class="proposal-badge">D-797 implementation</span><span>21 September 2026</span><span>' + doc.minutes + ' min read</span>' +
  '<a class="source-download" download="' + esc(doc.file) + '" href="data:text/plain;charset=utf-8,' + encodeURIComponent(doc.source) + '">Download source ↓</a></div></header>' +
  '<div class="article-body">' + doc.body + '</div><footer class="document-footer"><span>gocassini · Shaping notebook</span>' +
  (documents[i + 1] ? '<a href="#' + documents[i + 1].id + '">Next: ' + esc(documents[i + 1].label) + ' →</a>' : '<a href="#proposal">Back to proposal ↑</a>') +
  '</footer></article>').join('\n');

const output = '<!doctype html>\n<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">' +
  '<meta name="color-scheme" content="light"><meta name="description" content="Research and shaping for optional transcription, configuration-time model downloads, and persistent model storage in gocassini.">' +
  '<title>Optional transcription & persistent models — gocassini</title><style>' + css + '</style></head><body>' +
  '<a class="skip-link" href="#content">Skip to document</a><div class="reading-progress" aria-hidden="true"><span></span></div>' +
  '<aside class="sidebar" aria-label="Document navigation"><a class="brand" href="#proposal"><span class="brand-symbol" aria-hidden="true">g</span><span>gocassini<small>Shaping notebook</small></span></a>' +
  '<div class="sidebar-caption">THE DOCUMENTS</div><nav class="document-nav" aria-label="Documents">' + nav + '</nav>' +
  '<div class="section-navigation"><div class="sidebar-caption">ON THIS PAGE</div><nav id="section-nav" aria-label="Sections"></nav></div>' +
  '<div class="sidebar-footer"><span class="status-dot"></span> Working shape · B<br><small>Verified model revisions in persistent storage</small></div></aside>' +
  '<div class="workspace"><div class="topbar"><span class="breadcrumb">Product & engineering<span>/</span><strong id="current-document">Shaping proposal</strong></span>' +
  '<button class="print-button" type="button" id="print-all">Print all documents <span aria-hidden="true">↗</span></button></div>' +
  '<main id="content" tabindex="-1">' + articles + '</main><div class="site-footer">Prepared from the Markdown sources. Research snapshot <code>' + esc(commit.slice(0, 10)) + '</code>. Implementation and validation are recorded in the operator guide.</div></div>' +
  '<dialog id="diagram-dialog" aria-labelledby="diagram-title"><div class="dialog-toolbar"><h2 id="diagram-title">System wiring</h2>' +
  '<label class="zoom-control">Zoom <input id="diagram-zoom" type="range" min="25" max="150" value="100" step="5"><output id="zoom-value">100%</output></label>' +
  '<button class="print-button" type="button" id="close-diagram">Close <span aria-hidden="true">×</span></button></div><div id="large-diagram"></div></dialog>' +
  '<script>' + js + '</script></body></html>\n';
await writeFile(path.join(dir, 'index.html'), output);
console.log('Built index.html: ' + Buffer.byteLength(output).toLocaleString() + ' bytes; ' + documents.length + ' documents; offline styles, script, and SVG.');
