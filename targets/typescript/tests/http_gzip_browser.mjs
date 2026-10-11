// Actual browser execution of a generated library that uses lib/http against
// gzip responses. The browser's fetch decodes, so Go's raw bytes for a caller's
// Accept-Encoding, truncation errors and multi-member streams (Chromium keeps
// only the first member) are unavailable; the rest must match Go. Browsers
// cannot set client certificates or trust, or use Unix sockets, so DoWith options fail.
import { createServer } from 'node:http';
import { gzipSync } from 'node:zlib';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const tooling = resolve(process.env.TDF_BROWSER_TOOLING || 'out/typescript-host-operations/tooling/node_modules');
const { build } = await import(pathToFileURL(tooling + '/esbuild/lib/main.js'));
const { chromium } = await import(pathToFileURL(tooling + '/playwright/index.mjs'));
const generated = process.argv[2];
const suite = `import * as g from 'goalchemy-generated';
export async function run(base){
  const out={};
  for (const [name,accept] of [['/single',false],['/large',false],['/empty',false],['/explicit',true]]) out[name]=await g.Show(base,name,accept);
  out['/tls']=await g.ShowTLS(base);
  return out;
}`;
const result = await build({ stdin: { resolveDir: generated, contents: suite }, bundle: true, platform: 'browser', format: 'esm', write: false });
const single = gzipSync('goalchemy gzip body');
const bodies = {
  '/single': single,
  '/large': gzipSync('z'.repeat(4096)),
  '/empty': Buffer.alloc(0),
  '/explicit': single,
};
const server = createServer((req, res) => {
  if (req.url in bodies) {
    res.writeHead(200, { 'Content-Encoding': 'gzip', 'Content-Length': bodies[req.url].length });
    res.end(bodies[req.url]);
    return;
  }
  res.setHeader('Content-Type', req.url === '/suite.js' ? 'text/javascript' : 'text/html');
  res.end(req.url === '/suite.js' ? result.outputFiles[0].text :
    '<script type="module">import*as suite from"/suite.js";window.result=suite.run(location.origin);</script>');
});
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
let browser;
try {
  browser = await chromium.launch({
    ...(process.env.GOALCHEMY_CHROME ? { executablePath: process.env.GOALCHEMY_CHROME } : {}),
    headless: true, args: ['--no-sandbox'],
  });
  const page = await browser.newPage();
  const failures = [];
  page.on('pageerror', error => failures.push(String(error)));
  await page.goto('http://127.0.0.1:' + server.address().port);
  const got = await page.evaluate(() => window.result);
  const want = {
    '/single': '200 goalchemy gzip body encoding=',
    '/large': 'error: http: response body exceeds limit',
    '/empty': '200  encoding=gzip',
    '/explicit': 'error: http: Accept-Encoding is not supported in browsers',
    '/tls': 'error: http: transport options are not supported in browsers',
  };
  if (failures.length || JSON.stringify(got) !== JSON.stringify(want))
    throw new Error('browser gzip failures: ' + failures + '; got ' + JSON.stringify(got));
  console.log('PASS Chromium ' + await browser.version() + ': lib/http gzip decoding, header stripping, decoded limit, Accept-Encoding and transport option rejection');
} finally {
  await browser?.close();
  await new Promise(resolve => server.close(resolve));
}
