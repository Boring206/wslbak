// 建置與測試腳本。
//   node scripts/build.mjs          編出 bin/ 底下的四個執行檔（x64／arm64 × 主控台／無視窗）
//   node scripts/build.mjs --test   go vet 加上單元測試
//   node scripts/build.mjs --dist   把編好的執行檔包成 dist/ 底下的 zip、SHA256SUMS 與套件清單
//   node scripts/build.mjs --as <版本> --into <資料夾>
//                                   在那個資料夾編出標成另一個版本的 wslbak.exe 與 wslbakw.exe（測試升級用）
// 版本號以 package.json 為唯一來源，建置時注入執行檔。

import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, rmSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const pkg = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const WINDOWS_GO = '/mnt/c/Program Files/Go/bin/go.exe';

// PATH 上有 go 就用它；在 WSL 裡找不到時，退回 Windows 安裝的 go.exe。
function findGo() {
  if (process.env.GO) return process.env.GO;
  const probe = spawnSync('go', ['version'], { stdio: 'ignore' });
  if (!probe.error && probe.status === 0) return 'go';
  if (process.platform === 'linux' && existsSync(WINDOWS_GO)) return WINDOWS_GO;
  console.error('Go was not found. Install Go 1.26 or newer: https://go.dev/dl/');
  process.exit(1);
}

const go = findGo();
// 從 WSL 執行 Windows 的 go.exe 時，環境變數要列在 WSLENV 裡才傳得過去。
const viaInterop = process.platform === 'linux' && go.endsWith('.exe');

function run(cmd, args, env = {}) {
  const merged = { ...process.env, ...env };
  if (viaInterop) {
    merged.WSLENV = [process.env.WSLENV, ...Object.keys(env)].filter(Boolean).join(':');
  }
  const result = spawnSync(cmd, args, { cwd: root, stdio: 'inherit', env: merged });
  if (result.error) {
    console.error(`${cmd}: ${result.error.message}`);
    process.exit(1);
  }
  if (result.status !== 0) process.exit(result.status ?? 1);
}

const target = { GOOS: 'windows', CGO_ENABLED: '0' };
mkdirSync(join(root, 'bin'), { recursive: true });
// 測試用的工具與另一個版本的執行檔只編給這台機器的架構。
const hostArch = process.arch === 'arm64' ? 'arm64' : 'amd64';

function variantsFor(version) {
  const base = `-s -w -X main.version=${version}`;
  // wslbakw 是同一份程式編成沒有主控台視窗的版本，給排程工作用：
  // 主控台程式被工作排程器啟動時會跳出一個黑色視窗。
  return [
    ['wslbak', base],
    ['wslbakw', `${base} -H=windowsgui`],
  ];
}

// go.exe 是 Windows 程式，輸出位置要給它 Windows 路徑。
function forGo(path) {
  if (!viaInterop || !path.startsWith('/')) return path;
  const converted = spawnSync('wslpath', ['-w', path], { encoding: 'utf8' });
  return converted.status === 0 ? converted.stdout.trim() : path;
}

const asIndex = process.argv.indexOf('--as');
if (asIndex > 0) {
  const version = process.argv[asIndex + 1];
  const into = process.argv[process.argv.indexOf('--into') + 1];
  if (!version || !into || process.argv.indexOf('--into') < 0) {
    console.error('usage: node scripts/build.mjs --as <version> --into <folder>');
    process.exit(2);
  }
  mkdirSync(into, { recursive: true });
  for (const [prefix, ldflags] of variantsFor(version)) {
    run(go, ['build', '-trimpath', '-ldflags', ldflags, '-o', forGo(join(into, `${prefix}.exe`)), '.'], { ...target, GOARCH: hostArch });
  }
  process.exit(0);
}

if (process.argv.includes('--dist')) {
  // mkdist 是在這台機器上直接執行的小程式，不能帶著給 Windows 用的 GOOS。
  run(go, ['run', 'scripts/mkdist.go', pkg.version]);
} else if (process.argv.includes('--test')) {
  run(go, ['vet', './...'], target);
  // 先把測試編成執行檔再自己執行，不直接用 go test：
  // 防毒軟體掃描剛編好的 exe 時，go 會因為刪不掉暫存檔而回報失敗，即使測試全部通過。
  const testExe = join(root, 'bin', 'wslbak.test.exe');
  run(go, ['test', '-c', '-o', 'bin/wslbak.test.exe', '.'], target);
  run(testExe, []);
  try {
    rmSync(testExe, { force: true });
  } catch {
    // 檔案還被掃描中就留著，bin/*.exe 已列在 .gitignore，也不會被打包。
  }
} else {
  const variants = variantsFor(pkg.version);
  for (const [goarch, name] of [['amd64', 'x64'], ['arm64', 'arm64']]) {
    for (const [prefix, ldflags] of variants) {
      const out = `bin/${prefix}-${name}.exe`;
      run(go, ['build', '-trimpath', '-ldflags', ldflags, '-o', out, '.'], { ...target, GOARCH: goarch });
      console.log(`built ${out} (v${pkg.version})`);
    }
  }
  // 端對端測試用來在真正的主控台裡執行程式的小工具，不會被打包。
  run(go, ['build', '-trimpath', '-o', 'bin/e2e-console.exe', 'scripts/e2econsole.go'], { ...target, GOARCH: hostArch });
  console.log('built bin/e2e-console.exe (test helper)');
}
