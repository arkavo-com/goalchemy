import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const site = fileURLToPath(new URL('../', import.meta.url));
const root = path.resolve(site, '..');
const source = await readFile(path.join(site, 'src/examples/tasks.go.txt'), 'utf8');
const directory = await mkdtemp(path.join(tmpdir(), 'goalchemy-site-example-'));
const targets = { go: '.go', typescript: '.ts', python: '.py', java: '.java', csharp: '.cs', rust: '.rs', c: '.c', swift: '.swift' };

try {
  const compiler = path.join(directory, 'goalchemy');
  execFileSync('go', ['build', '-o', compiler, './cmd/goalchemy'], { cwd: root, stdio: 'inherit' });
  await writeFile(path.join(directory, 'go.mod'), `module example.com/tasks\n\ngo 1.25\n\nrequire github.com/eugenioenko/goalchemy v0.0.0\n\nreplace github.com/eugenioenko/goalchemy => ${JSON.stringify(root)}\n`);
  await writeFile(path.join(directory, 'main.go'), source);
  const outputs = {};
  for (const [target, extension] of Object.entries(targets)) {
    const out = path.join(directory, target);
    const pauseModel = target === 'c' ? 'frames' : 'native';
    execFileSync(compiler, ['compile', '-gate', 'cooperative', '-target', target, '-out', out,
      '--pause-model', pauseModel, '--names=source', '--line-directives=false', '.'], { cwd: directory, stdio: 'inherit' });
    const manifest = JSON.parse(await readFile(path.join(out, 'goalchemy.manifest.json'), 'utf8'));
    const file = manifest.source_packages.find((pkg) => pkg.root).files.find((file) => file.endsWith(extension));
    if (!file) throw new Error(`No source package output found for ${target}`);
    outputs[target] = { file, pauseModel, code: await readFile(path.join(out, file), 'utf8') };
    console.log(`Generated ${target}: ${file}`);
  }
  const generated = path.join(site, 'src/examples/generated');
  await mkdir(generated, { recursive: true });
  const metadata = {};
  for (const [target, output] of Object.entries(outputs)) {
    await writeFile(path.join(generated, `${target}.txt`), output.code);
    metadata[target] = { file: output.file, pauseModel: output.pauseModel };
  }
  const snapshot = { sourceHash: createHash('sha256').update(source).digest('hex'), names: 'source', lineDirectives: false, outputs: metadata };
  await writeFile(path.join(site, 'src/examples/generated.json'), JSON.stringify(snapshot, null, 2) + '\n');
} finally {
  await rm(directory, { recursive: true, force: true });
}
