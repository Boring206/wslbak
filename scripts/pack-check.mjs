// Run on Linux right before "npm publish": marks the four executables as executable and
// stops unless npm would pack them that way.
//
//   node scripts/pack-check.mjs
//
// The executables are built on Windows and reach the Linux runner that publishes through an
// artifact, which does not keep file modes: they arrive as 0644, and npm packs files with the
// mode they have. 0.1.1 was published like that. Inside WSL the launcher (bin/wslbak.js) then
// sets the execute bit itself on every run, which it cannot do when the package was installed
// with sudo; the user is told to run chmod by hand. On Windows there is no such mode to set,
// so this check fails there: publish from Linux.

import { spawnSync } from 'node:child_process';
import { chmodSync, readFileSync, readdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const pkg = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const exes = pkg.files.filter((name) => name.endsWith('.exe'));
// The launcher is checked as well: git records it as executable, and it must stay so.
const wanted = [...exes, ...Object.values(pkg.bin)];

for (const name of exes) chmodSync(join(root, name), 0o755);

// npm shows one README on the package page: the first file named README or README.* that its
// search of this folder returns, in an order that depends on the file system. With two such
// files the page of 0.1.1 came out in Chinese only. The Chinese README is therefore called
// README-zh-TW.md, and nothing else here may match.
const readmes = readdirSync(root).filter((name) => /^readme(\..*)?$/i.test(name));
if (readmes.length !== 1 || readmes[0] !== 'README.md') {
  console.error(`README.md has to be the only file named README or README.* here; found: ${readmes.join(', ') || 'none'}`);
  process.exit(1);
}

// What npm reports here is what ends up in the tarball: it packs each file with the mode it
// has on disk, less the write bits for group and others.
const packed = spawnSync('npm', ['pack', '--dry-run', '--json', '--ignore-scripts'], {
  cwd: root,
  encoding: 'utf8',
  shell: process.platform === 'win32',
});
if (packed.status !== 0) {
  console.error(packed.stderr || packed.error);
  process.exit(1);
}
const files = JSON.parse(packed.stdout)[0].files;

let wrong = 0;
for (const name of wanted) {
  const entry = files.find((file) => file.path === name);
  const mode = entry ? (entry.mode & 0o777).toString(8) : 'missing';
  console.log(`${mode.padStart(7)}  ${name}`);
  if (mode !== '755') wrong++;
}
if (exes.length !== 4 || wrong > 0) {
  console.error('The executables and the launcher have to be in the package with mode 755.');
  process.exit(1);
}
