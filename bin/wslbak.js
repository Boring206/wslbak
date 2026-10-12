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

// 補回 Windows 互通那筆設定的指令。指名目前這個 distro：不指名的話，wsl.exe 會到預設的 distro 去執行，
// 它如果沒在執行，會被啟動、隨後又停止，可能再把設定清掉一次。名稱只接受 WSL 允許的字元，其餘的就不帶。
const here = /^[\w.-]+$/.test(process.env.WSL_DISTRO_NAME || '') ? `-d ${process.env.WSL_DISTRO_NAME} ` : '';
const repairInterop =
  `  wsl ${here}-u root sh -c "echo ':WSLInterop:M::MZ::/init:P' > /proc/sys/fs/binfmt_misc/register"\n`;

const messages = {
  en: {
    prefix: 'wslbak: ',
    unsupported: 'only Windows and WSL are supported.',
    missing: (exe) => `cannot find ${exe}\nWhen installing from source, run npm run build first.`,
    noExec: (exe) => `the executable is not marked executable. Run: sudo chmod +x "${exe}"`,
    noInterop:
      'this WSL cannot start Windows programs right now.\n' +
      'The kernel setting that hands .exe files to Windows is missing or switched off.\n' +
      'In a WSL 2 distro, a distro that shuts down can remove it for every distro. A missing one is put back from a Windows terminal with\n' +
      repairInterop +
      'Running wsl --shutdown there and reopening the distro restores it in either case.\n' +
      'In a WSL 1 distro, the setting is missing when /etc/wsl.conf has [interop] enabled=false.\n' +
      'You can also install wslbak with Node on Windows instead.',
    noInit: (exe) =>
      `cannot start ${exe}\n` +
      'The system reports a missing file, and the executable itself is there.\n' +
      'What is missing, then, is the program that the kernel hands .exe files to (/init in WSL): it cannot be reached from here.',
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
      '這個 WSL 現在無法執行 Windows 程式。\n' +
      '核心裡把 .exe 交給 Windows 的那筆設定不見了，或被停用了。\n' +
      'WSL 2 的 distro：有些 distro 關閉時會把它清掉，所有 distro 都受影響。不見了的話，在 Windows 的終端機執行下面這一行可以補回來：\n' +
      repairInterop +
      '兩種情況都可以在那裡執行 wsl --shutdown，再重新開啟 distro 來恢復。\n' +
      'WSL 1 的 distro：/etc/wsl.conf 設了 [interop] enabled=false 的時候，就不會有這筆設定。\n' +
      '也可以改用 Windows 上的 Node 安裝 wslbak。',
    noInit: (exe) =>
      `無法啟動 ${exe}\n` +
      '系統回報找不到檔案，但執行檔本身在。\n' +
      '那麼找不到的是核心用來執行 .exe 的那支程式（WSL 裡是 /init）：從這裡碰不到它。',
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

// WSL 靠核心裡的一筆設定（binfmt_misc）把 .exe 交給 Windows 執行。那筆設定不在的時候（另一個 distro
// 關閉時把整張表清掉了），或被人停用的時候，啟動 exe 並不會得到明確的錯誤：glibc 會改拿 /bin/sh 去讀
// 那支 exe，印出一串亂碼。所以先看它在不在；看不出來（那個資料夾讀不到）就照舊往下走。
// 在 /etc/wsl.conf 關掉互通（[interop] enabled=false）的結果，兩種 WSL 不一樣（都實測過）：
// WSL 1 每個 distro 有自己的一筆設定，關掉互通就不會有，這裡看得出來；
// WSL 2 的設定是整台虛擬機共用的，關掉互通它還在，這裡看不出來，WSL 自己會在大約十秒後印出錯誤。
function cannotStartWindowsPrograms() {
  const dir = '/proc/sys/fs/binfmt_misc';
  try {
    const names = fs.readdirSync(dir);
    // 沒有 register 表示這個資料夾沒有掛上，裡面什麼都看不到。
    if (!names.includes('register')) return false;
    if (/^disabled/.test(fs.readFileSync(path.join(dir, 'status'), 'utf8'))) return true;
    for (const name of names) {
      if (name === 'register' || name === 'status') continue;
      const entry = fs.readFileSync(path.join(dir, name), 'utf8');
      // 啟用中、認得 MZ 開頭（4d5a）的那一筆。WSL 自己的叫 WSLInterop 或 WSLInterop-late；
      // 叫這個名字又啟用著的，內容長得不一樣也算數，寧可照舊往下走。
      if (/^enabled$/m.test(entry) && (/^magic 4d5a/m.test(entry) || name.startsWith('WSLInterop'))) return false;
    }
    return true;
  } catch {
    return false;
  }
}
if (onLinux && cannotStartWindowsPrograms()) fail(text.noInterop);

const forwarded = onLinux ? translatePaths(args) : [...args];
// 從 WSL 啟動的 Windows 程式看不到 WSL 的環境變數，所以把 WSLBAK_LANG 轉成參數。
if (knownLanguage && !args.some((a) => a === '--lang' || a.startsWith('--lang='))) {
  forwarded.unshift('--lang', explicit);
}

const result = spawnSync(exe, forwarded, { stdio: 'inherit' });
if (result.error) {
  const code = result.error.code;
  if (onLinux && code === 'EACCES') fail(text.noExec(exe));
  if (onLinux && code === 'ENOEXEC') fail(text.noInterop);
  // exe 在（前面確認過），卻回報找不到檔案：找不到的是那筆設定指名的程式。
  if (onLinux && code === 'ENOENT') fail(text.noInit(exe));
  if (!onLinux && (code === 'EACCES' || code === 'EPERM' || code === 'UNKNOWN')) fail(text.blocked(exe));
  fail(result.error.message);
}
process.exit(result.status === null ? 2 : result.status);
