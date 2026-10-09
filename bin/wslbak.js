#!/usr/bin/env node
'use strict';

// wslbak 的啟動器：套件裡放的是編好的 Windows 執行檔，這支只負責挑對架構並把參數轉交過去。
// 在 WSL 裡安裝時，同一支 exe 會透過 WSL 的 Windows 互通（interop）執行。

const { spawnSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');

const args = process.argv.slice(2);

// --lang 參數或 WSLBAK_LANG 明確指定的語言；沒有指定時回傳空字串。
function requestedLanguage() {
  let value = '';
  args.forEach((a, i) => {
    // 重複出現時以最後一個為準，和執行檔的行為一致。
    if (a === '--lang' && args[i + 1]) value = args[i + 1];
    else if (a.startsWith('--lang=')) value = a.slice('--lang='.length);
  });
  return value || process.env.WSLBAK_LANG || '';
}

const explicit = requestedLanguage().toLowerCase().replace(/_/g, '-');
const knownLanguage = /^(en|zh)(-|$)/.test(explicit);

// 啟動器自己的錯誤訊息用哪種語言：有明確指定就照指定，否則看 Node 回報的地區設定。
function useChinese() {
  if (knownLanguage) return explicit.startsWith('zh');
  let locale = '';
  try {
    locale = Intl.DateTimeFormat().resolvedOptions().locale.toLowerCase();
  } catch {
    // 讀不到地區設定就用英文。
  }
  return /^zh-(hant|tw|hk|mo)/.test(locale);
}

const messages = {
  en: {
    prefix: 'wslbak: ',
    unsupported: 'only Windows and WSL are supported.',
    missing: (exe) => `cannot find ${exe}\nWhen installing from source, run npm run build first.`,
    noExec: (exe) => `the executable is not marked executable. Run: sudo chmod +x "${exe}"`,
    noInterop:
      'this WSL cannot run Windows programs (interop is disabled).\n' +
      'Check that /etc/wsl.conf does not set [interop] enabled=false,\n' +
      'then run wsl --shutdown on Windows and reopen the distro.\n' +
      'You can also install wslbak with Node on Windows instead.',
    blocked: (exe) =>
      `Windows refused to start ${exe}\n` +
      'The program is not code-signed, so Smart App Control, an antivirus, or an AppLocker policy may be blocking it.\n' +
      'See "Troubleshooting" in the README.',
  },
  zh: {
    prefix: 'wslbak：',
    unsupported: '只支援 Windows 與 WSL。',
    missing: (exe) => `找不到執行檔 ${exe}\n從原始碼安裝的話，請先執行 npm run build。`,
    noExec: (exe) => `沒有執行權限，請執行：sudo chmod +x "${exe}"`,
    noInterop:
      '這個 WSL 無法執行 Windows 程式（interop 沒有啟用）。\n' +
      '請確認 /etc/wsl.conf 沒有設定 [interop] enabled=false，\n' +
      '修改後在 Windows 執行 wsl --shutdown 再重新開啟。\n' +
      '也可以改用 Windows 上的 Node 安裝 wslbak。',
    blocked: (exe) =>
      `Windows 拒絕執行 ${exe}\n` +
      '這支程式沒有程式碼簽章，可能被智慧型應用程式控制、防毒軟體或 AppLocker 原則擋下。\n' +
      '請參考 README 的「疑難排解」。',
  },
};
const text = useChinese() ? messages.zh : messages.en;

function fail(message) {
  process.stderr.write(`${text.prefix}${message}\n`);
  process.exit(2);
}

function isWSL() {
  if (process.env.WSL_DISTRO_NAME) return true;
  try {
    return /microsoft/i.test(fs.readFileSync('/proc/version', 'utf8'));
  } catch {
    return false;
  }
}

const onLinux = process.platform === 'linux';
if (process.platform !== 'win32' && !(onLinux && isWSL())) {
  fail(text.unsupported);
}

const arch = process.arch === 'arm64' ? 'arm64' : 'x64';
const exe = path.join(__dirname, `wslbak-${arch}.exe`);
if (!fs.existsSync(exe)) {
  fail(text.missing(exe));
}

if (onLinux) {
  // 套件若是在 Windows 上打包的，exe 不會帶執行權限。
  for (const name of [`wslbak-${arch}.exe`, `wslbakw-${arch}.exe`]) {
    try {
      fs.chmodSync(path.join(__dirname, name), 0o755);
    } catch {
      // 用 sudo 安裝時改不了權限；下面執行失敗時會提示怎麼處理。
    }
  }
}

// 值是路徑的旗標。在 WSL 裡使用者會打 Linux 路徑（/mnt/d/backup），Windows 程式看不懂，先轉好。
const PATH_FLAGS = ['--dest', '--to', '--home'];

function toWindowsPath(value) {
  // 已經是 Windows 路徑（D:\…、D:/…、\\server\share）就不動。
  if (/^[A-Za-z]:[\\/]/.test(value) || value.startsWith('\\\\')) return value;
  const converted = spawnSync('wslpath', ['-a', '-w', value], { encoding: 'utf8' });
  if (converted.error || converted.status !== 0) return value;
  return converted.stdout.trim() || value;
}

function translatePaths(list) {
  const out = [];
  for (let i = 0; i < list.length; i++) {
    const a = list[i];
    const eq = a.indexOf('=');
    if (PATH_FLAGS.includes(a) && i + 1 < list.length) {
      out.push(a, toWindowsPath(list[++i]));
    } else if (eq > 0 && PATH_FLAGS.includes(a.slice(0, eq))) {
      out.push(`${a.slice(0, eq)}=${toWindowsPath(a.slice(eq + 1))}`);
    } else {
      out.push(a);
    }
  }
  return out;
}

const forwarded = onLinux ? translatePaths(args) : [...args];
// 從 WSL 啟動的 Windows 程式看不到 WSL 的環境變數，所以把 WSLBAK_LANG 轉成參數。
if (knownLanguage && !args.some((a) => a === '--lang' || a.startsWith('--lang='))) {
  forwarded.unshift('--lang', explicit);
}

const result = spawnSync(exe, forwarded, { stdio: 'inherit' });
if (result.error) {
  const code = result.error.code;
  if (onLinux && code === 'EACCES') fail(text.noExec(exe));
  if (onLinux && (code === 'ENOEXEC' || code === 'ENOENT')) fail(text.noInterop);
  if (!onLinux && (code === 'EACCES' || code === 'EPERM' || code === 'UNKNOWN')) fail(text.blocked(exe));
  fail(result.error.message);
}
process.exit(result.status === null ? 2 : result.status);
