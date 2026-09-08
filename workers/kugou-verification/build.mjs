import { readFile, mkdir, writeFile } from 'node:fs/promises';

// Bundle the small page and browser WASM as bytes, without build dependencies.
// WASM runs in the user's browser, never in the Worker.
const files = {
  '/': ['index.html', 'text/html; charset=utf-8'],
  '/client.js': ['client.js', 'application/javascript; charset=utf-8'],
  '/style.css': ['style.css', 'text/css; charset=utf-8'],
  '/verifycode.js': ['verifycode.js', 'application/javascript; charset=utf-8'],
  '/verifycode_bg_ios.wasm': ['verifycode_bg_ios.wasm', 'application/wasm'],
  '/VERIFYCODE-LICENSE.txt': ['VERIFYCODE-LICENSE.txt', 'text/plain; charset=utf-8'],
};
const assets = {};
for (const [path, [file, type]] of Object.entries(files)) {
  assets[path] = { type, data: (await readFile(new URL(`public/${file}`, import.meta.url))).toString('base64') };
}
const source = await readFile(new URL('worker.mjs', import.meta.url), 'utf8');
await mkdir(new URL('dist/', import.meta.url), { recursive: true });
await writeFile(new URL('dist/worker.mjs', import.meta.url), `const ASSETS = ${JSON.stringify(assets)};\n${source}`);
