import { copyFile, mkdir, readFile, writeFile } from 'node:fs/promises';

await mkdir('html/vendor', { recursive: true });
const hls = await readFile('node_modules/hls.js/dist/hls.min.js', 'utf8');
await writeFile('html/vendor/hls.min.js', hls.replace(/\r?\n?\/\/# sourceMappingURL=[^\r\n]*/u, ''), 'utf8');
await copyFile('node_modules/hls.js/LICENSE', 'html/vendor/hls.LICENSE');
