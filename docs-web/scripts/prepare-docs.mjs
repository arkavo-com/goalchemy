import { copyFile, mkdir, readFile, rm, stat, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import GithubSlugger from 'github-slugger';
import { unified } from 'unified';
import remarkParse from 'remark-parse';
import remarkGfm from 'remark-gfm';
import remarkStringify from 'remark-stringify';
import { parse, stringify } from 'yaml';
import { sources } from './sources.mjs';

const site = fileURLToPath(new URL('../', import.meta.url));
const root = path.resolve(site, '..');
const content = path.join(site, 'src/content/docs');
const repository = 'https://github.com/eugenioenko/goalchemy';
const markdown = unified().use(remarkParse).use(remarkGfm).use(remarkStringify, { fences: true, bullet: '-' });
const pages = [];
const routes = new Map(sources.map(([source, route]) => [source, `/${route}/`]));
const libraryAnchors = new Map();
const headingText = (node) => node.value ?? node.children?.map(headingText).join('') ?? '';
const read = (source) => readFile(path.join(root, source), 'utf8');

for (const [source, route, title, description, order] of sources) {
  const tree = markdown.parse(await read(source));
  tree.children = tree.children.filter((node, index) => !(index === 0 && node.type === 'heading' && node.depth === 1));
  pages.push({ source, route, title, description, order, tree });
}

// Split the generated API reference by package while keeping its original anchor links usable.
const library = markdown.parse(await read('docs/library.md'));
const originalSlugs = new GithubSlugger();
for (const node of library.children) {
  if (node.type === 'heading') node.originalSlug = originalSlugs.slug(headingText(node));
}
const standardStart = library.children.findIndex((node) => node.type === 'heading' && headingText(node) === 'Standard packages');
const capabilityStart = library.children.findIndex((node) => node.type === 'heading' && headingText(node) === 'Capability packages');
if (standardStart < 0 || capabilityStart < 0) throw new Error('Runtime reference structure changed; update the publication splitter.');
const firstCapability = library.children.findIndex((node, index) => index > capabilityStart && node.type === 'heading' && node.depth === 2 && headingText(node) !== 'Native dependencies');
const overview = [...library.children.slice(0, standardStart), ...library.children.slice(capabilityStart, firstCapability)];
const overviewSlugs = new GithubSlugger();
for (const node of overview) {
  if (node.type !== 'heading') continue;
  if (headingText(node) === 'Runtime library reference') {
    libraryAnchors.set(node.originalSlug, '/runtime/overview/');
  } else {
    if (node.depth === 1) node.depth = 2;
    libraryAnchors.set(node.originalSlug, `/runtime/overview/#${overviewSlugs.slug(headingText(node))}`);
  }
}
libraryAnchors.set('standard-packages', '/runtime/overview/#packages');
pages.push({ source: 'docs/library.md', route: 'runtime/overview', title: 'Runtime library', description: 'Standard packages, native capabilities, dependencies, and the complete package index.', order: 1,
  tree: { type: 'root', children: overview.filter((node) => !(node.type === 'heading' && headingText(node) === 'Runtime library reference')) } });
routes.set('docs/library.md', '/runtime/overview/');

for (const [start, end, layer] of [[standardStart + 1, capabilityStart, 'std'], [firstCapability, library.children.length, 'lib']]) {
  for (let index = start; index < end; index++) {
    const node = library.children[index];
    if (node.type !== 'heading' || node.depth !== 2) continue;
    const name = headingText(node).replace(/^std\//, '');
    let next = index + 1;
    while (next < end && !(library.children[next].type === 'heading' && library.children[next].depth === 2)) next++;
    const route = `runtime/${layer}/${name}`;
    libraryAnchors.set(node.originalSlug, `/${route}/`);
    const children = library.children.slice(index + 1, next);
    const slugs = new GithubSlugger();
    for (const child of children) {
      if (child.type !== 'heading') continue;
      child.depth = Math.max(2, child.depth - 1);
      libraryAnchors.set(child.originalSlug, `/${route}/#${slugs.slug(headingText(child))}`);
    }
    pages.push({ source: 'docs/library.md', route, title: `${layer}/${name}`, description: `API reference for github.com/eugenioenko/goalchemy/${layer}/${name}.`, order: 1, tree: { type: 'root', children } });
    index = next - 1;
  }
}

// References to a separate SDK checkout are prose in the public site, not broken repository links.
let adjacentReferences = 0;
async function rewrite(node, source) {
  // Go doc comments contain literal tags such as <script> and <nil>, not site HTML.
  if (node.type === 'html' && !node.value.startsWith('<!--')) node.type = 'text';
  if ((node.type === 'link' || node.type === 'definition' || node.type === 'image') && !/^(?:[a-z][a-z\d+.-]*:|\/\/)/i.test(node.url)) {
    const [pathname, fragment = ''] = node.url.split('#');
    const target = path.posix.normalize(path.posix.join(path.posix.dirname(source), decodeURIComponent(pathname || path.posix.basename(source))));
    if (target.startsWith('../')) {
      adjacentReferences++;
      // These links describe evidence in a sibling SDK checkout, which is not part of this publication.
      if (node.type !== 'link') throw new Error(`Unsupported out-of-repository reference: ${node.url}`);
      node.type = 'emphasis';
      delete node.url;
    } else if (target === 'docs/library.md' && fragment && libraryAnchors.has(fragment)) {
      node.url = libraryAnchors.get(fragment);
    } else if (routes.has(target)) {
      node.url = `${routes.get(target)}${fragment ? `#${fragment}` : ''}`;
    } else {
      let info;
      try { info = await stat(path.join(root, target)); } catch { throw new Error(`Missing reference in ${source}: ${node.url}`); }
      node.url = `${repository}/${info.isDirectory() ? 'tree' : 'blob'}/main/${target}${fragment ? `#${fragment}` : ''}`;
    }
  }
  for (const child of node.children ?? []) await rewrite(child, source);
}

await rm(path.join(content, 'runtime'), { recursive: true, force: true });
await rm(path.join(content, 'targets/details'), { recursive: true, force: true });
for (const page of pages) {
  await rewrite(page.tree, page.source);
  const metadata = {
    title: page.title,
    description: page.description,
    sidebar: { order: page.order },
    editUrl: `${repository}/edit/main/${page.source}`,
  };
  const note = `<!-- Published from ${page.source}. Edit the repository source, not this generated file. -->\n\n`;
  const destination = path.join(content, `${page.route}.md`);
  await mkdir(path.dirname(destination), { recursive: true });
  await writeFile(destination, `---\n${stringify(metadata)}---\n\n${note}${markdown.stringify(page.tree)}`);
}

const features = parse(await read('specs/features.yaml'));
const targets = ['go', 'typescript', 'python', 'java', 'csharp', 'rust', 'c', 'swift'];
const matrix = features.features.map((feature) => `| \`${feature.id}\` | ${feature.gate} | ${targets.map((target) => feature.targets.includes(target) ? feature.status : 'rejected').join(' | ')} |`).join('\n');
await mkdir(path.join(content, 'language'), { recursive: true });
await writeFile(path.join(content, 'language/features.md'), `---\n${stringify({ title: 'Feature matrix', description: 'Language support by feature gate and target, published from the checked feature manifest.', sidebar: { order: 3 }, editUrl: `${repository}/edit/main/specs/features.yaml` })}---\n\nPublished from [the checked feature manifest](${repository}/blob/main/specs/features.yaml). A supported feature has conformance fixtures for the listed targets. Swift remains experimental; feature coverage does not establish Apple platform support.\n\nRun \`goalchemy features\` for your installed compiler.\n\n| Feature | Gate | Go | TypeScript | Python | Java | C# | Rust | C | Swift |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n${matrix}\n`);
await copyFile(path.join(root, 'docs/goalchemy.png'), path.join(site, 'public/social.png'));
await copyFile(path.join(site, 'src/assets/goalchemy.svg'), path.join(site, 'public/favicon.svg'));
console.log(`Prepared ${pages.length + 1} documentation pages from repository sources (${adjacentReferences} sibling-SDK references rendered as prose).`);
