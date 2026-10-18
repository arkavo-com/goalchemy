import { readFile, readdir, stat } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse } from 'parse5';

const dist = fileURLToPath(new URL('../dist/', import.meta.url));
const origin = 'https://goalchemy.dev';
const pages = new Map();
const failures = [];

async function collect(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const file = path.join(directory, entry.name);
    if (entry.isDirectory()) await collect(file);
    else if (entry.name.endsWith('.html')) {
      const ids = new Set();
      const links = [];
      function visit(node) {
        const attrs = Object.fromEntries((node.attrs ?? []).map(({ name, value }) => [name, value]));
        if (attrs.id) ids.add(attrs.id);
        if (node.tagName === 'a' && attrs.href) links.push(attrs.href);
        if (attrs['data-example-url']) links.push(attrs['data-example-url']);
        if (['img', 'script', 'source'].includes(node.tagName) && attrs.src) links.push(attrs.src);
        if (node.tagName === 'link' && attrs.href && attrs.rel !== 'canonical') links.push(attrs.href);
        for (const child of node.childNodes ?? []) visit(child);
      }
      visit(parse(await readFile(file, 'utf8')));
      pages.set(file, { ids, links });
    }
  }
}

await collect(dist);
const llms = await readFile(path.join(dist, 'llms.txt'), 'utf8');
const llmsLinks = [...llms.matchAll(/^- \[[^\]]+\]\((https:\/\/[^)]+)\)/gm)].map((match) => match[1]);
const repositoryPrefixes = [
  'https://raw.githubusercontent.com/eugenioenko/goalchemy/main/',
  'https://github.com/eugenioenko/goalchemy/blob/main/',
  'https://github.com/eugenioenko/goalchemy/tree/main/',
];
for (const link of llmsLinks) {
  const prefix = repositoryPrefixes.find((prefix) => link.startsWith(prefix));
  if (!prefix) continue;
  try {
    await stat(fileURLToPath(new URL(`../../${link.slice(prefix.length)}`, import.meta.url)));
  } catch {
    failures.push(`llms.txt: missing repository path ${link}`);
  }
}
pages.set(path.join(dist, 'llms.txt'), { ids: new Set(), links: llmsLinks });
let checked = 0;
for (const [file, { links }] of pages) {
  const pathname = path.relative(dist, file).split(path.sep).join('/').replace(/index\.html$/, '');
  for (const link of links) {
    const url = new URL(link, `${origin}/${pathname}`);
    if (url.origin !== origin) continue;
    checked++;
    let target = path.join(dist, decodeURIComponent(url.pathname));
    try {
      if ((await stat(target)).isDirectory()) target = path.join(target, 'index.html');
      await stat(target);
      if (url.hash && pages.has(target) && !pages.get(target).ids.has(decodeURIComponent(url.hash.slice(1)))) {
        failures.push(`${pathname}: missing anchor ${link}`);
      }
    } catch {
      failures.push(`${pathname}: missing file ${link}`);
    }
  }
}
if ((await readFile(path.join(dist, 'CNAME'), 'utf8')).trim() !== 'goalchemy.dev') failures.push('CNAME must contain goalchemy.dev');
if (!(await readFile(path.join(dist, 'sitemap-0.xml'), 'utf8')).includes(`${origin}/`)) failures.push('Sitemap must use the production domain');
if (failures.length) {
  console.error([...new Set(failures)].join('\n'));
  process.exitCode = 1;
} else console.log(`Checked ${checked} internal links and assets across ${pages.size - 1} HTML pages, including fragment targets, llms.txt references, CNAME, and sitemap.`);
