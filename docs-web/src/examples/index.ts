import { createHash } from 'node:crypto';
import { codeToHtml } from 'shiki';
import source from './tasks.go.txt?raw';
import snapshot from './generated.json';

if (createHash('sha256').update(source).digest('hex') !== snapshot.sourceHash) {
  throw new Error('Homepage examples are stale. Run pnpm --filter docs-web examples:generate.');
}

export { source };
const generated = import.meta.glob<string>('./generated/*.txt', { eager: true, query: '?raw', import: 'default' });
export const examples = [
  { id: 'go', label: 'Go', lang: 'go' },
  { id: 'typescript', label: 'TypeScript', lang: 'typescript' },
  { id: 'python', label: 'Python', lang: 'python' },
  { id: 'java', label: 'Java', lang: 'java' },
  { id: 'csharp', label: 'C#', lang: 'csharp' },
  { id: 'rust', label: 'Rust', lang: 'rust' },
  { id: 'c', label: 'C', lang: 'c' },
  { id: 'swift', label: 'Swift', lang: 'swift' },
].map((target) => ({ ...target, ...snapshot.outputs[target.id as keyof typeof snapshot.outputs], code: generated[`./generated/${target.id}.txt`]! }));

export const highlight = (code: string, lang: string) => codeToHtml(code, {
  lang,
  themes: { light: 'github-light', dark: 'github-dark' },
  defaultColor: false,
  tabindex: -1,
});
