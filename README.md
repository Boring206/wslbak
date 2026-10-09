# wslbak

**English** | [繁體中文](README.zh-TW.md)

Scheduled backups of a WSL distro that do not stop it, a test restore after every backup, a
notification when something fails, and a one-line restore.

Files inside WSL are not covered by OneDrive or most backup tools: they live in one virtual disk, and
when that disk is damaged or Windows is reinstalled, everything in it is gone. The usual answer is a
scheduled `wsl --export`, but `wsl --export` terminates the distro before it exports, so your shells,
dev servers and containers die every time it runs. `wslbak` reads the distro from the inside instead,
while it keeps running:

```
> wslbak init
> wslbak run
Backing up Ubuntu-24.04 → D:\WSLBackup\Ubuntu-24.04
  wrote 20260115T030000Z.tar.gz (7.2 GB, 1 min 48 s)
  1 files or folders changed while being read (normal for a backup of a running system).
Test restore…
  test restore passed: all 512 sampled files match (1 min 33 s)
Done.
```

- **The distro is never stopped.** A root `tar` inside the distro streams it out; WSL is not shut down
  and nothing in the distro is written to.
- **Every backup is test-restored.** The archive is imported as a temporary distro, checked, and removed.
- **Standard format.** A backup is a plain `.tar.gz` that `wsl --import` accepts, with or without wslbak.
- **One command to set up**: a daily task, how many backups to keep, where they go.
- **Restore never overwrites.** It always creates a new distro next to whatever you have.

## Install

Requires Node.js 18 or newer, Windows 10 or 11, and the Microsoft Store version of WSL (`wsl --version`
must work; run `wsl --update` if it does not). Install from a Windows terminal or from inside WSL:

```
npm install -g wslbak
```

The package ships prebuilt Windows executables (x64 and arm64), so Go is not needed. When installed
inside WSL, the same executable runs through WSL's Windows interop.

## Getting started

```
wslbak init
```

`init` looks at your distros, proposes a destination, and then shows everything it is about to create
— the files, the registry key, the scheduled task and its exact command line — before asking
`Proceed? [y/N]`. Nothing is changed until you say yes; `--dry-run` shows the same plan and stops.
No administrator rights are needed.

By default backups go to `<drive>:\WSLBackup` on the internal drive with the most free space other
than the one holding the distro, so that they survive reinstalling Windows. An external drive or a
NAS share (`--dest`) also protects against the disk itself failing; `init` tells you when the
destination is on the same physical disk as the distro.

## Usage

```
wslbak init              set up backups and the daily task
wslbak run               back up now, test-restore, prune old backups
wslbak list              list backups
wslbak status            schedule, last result, and whether the installed program is intact
wslbak verify [id]       test-restore an existing backup again (default: the newest)
wslbak restore [id]      restore a backup as a new distro (default: the newest verified one)
wslbak uninstall         remove the task and the installed program; backups are kept

  -d, --distro <name>    which distro (default: the only one that can be backed up)
      --dest <folder>    init: where to store backups
      --keep <count>     init: how many verified backups to keep (default 7)
      --at <HH:MM>       init: time of the daily backup (default 03:00)
      --webhook <url>    init: also report failures to this URL
      --no-verify        run: skip the test restore this time
      --name <name>      restore: name of the restored distro
      --to <folder>      restore: where to put the restored distro
  -n, --dry-run          only show what would be done
  -y, --yes              do not ask for confirmation
      --lang <lang>      interface language: en or zh-TW
      --debug            show details and timing of each step
```

Exit codes: `0` success; `1` the backup was written but not verified, or `status` found the backups
stale; `2` failure; `3` another wslbak is already running.

Inside WSL you can give Linux paths (`--dest /mnt/d/WSLBackup`); they are converted for you.

## Restoring

```
wslbak restore
```

restores the newest backup that passed its test restore. If a distro with the original name still
exists, the copy is called `<name>-restored-<date>`; pick your own with `--name`. An existing distro
is never overwritten, changed or removed, and the restored one is not started for you.

**After reinstalling Windows**, you do not need Node or npm. Every backup run puts a copy of the
program and a `README-RESTORE.txt` into the backup folder. Install WSL, then:

```
D:\WSLBackup\wslbak.exe restore
```

**Without wslbak at all**, a backup is an ordinary archive:

```
wsl --import Ubuntu-24.04 C:\WSL\Ubuntu-24.04 D:\WSLBackup\Ubuntu-24.04\20260115T030000Z.tar.gz --version 2
```

A distro imported by hand logs in as root; add `[user]` and `default=<your user name>` to its
`/etc/wsl.conf` to change that. `wslbak restore` does this for you.

## What is backed up

Everything on the distro's root file system, with ownership, permissions, extended attributes, file
capabilities, hard links and sparse files. Left out by default: `/tmp/*`, `/var/tmp/*`, and the
`.cache` folders in home directories. Windows drives under `/mnt` are not part of the distro and are
never included.

Settings live in `%LOCALAPPDATA%\wslbak\config.json`:

```json
{
  "schema": 1,
  "at": "03:00",
  "verify": "restore",
  "notify": { "toast": true, "webhook": "", "on": "failure" },
  "distros": {
    "Ubuntu-24.04": {
      "id": "{…}",
      "dest": "D:\\WSLBackup",
      "keep": 7,
      "exclude": ["./home/*/Downloads/*"],
      "defaultExcludes": true,
      "enabled": true
    }
  }
}
```

- `exclude` takes `tar --exclude` patterns relative to `/`, written with a leading `./`.
- `verify: "none"` turns the test restore off; then the newest `keep` backups are kept regardless.
- `notify.on: "always"` also notifies on success, so that silence means the task is not running.
- Run `wslbak init -d <other distro>` to add another distro. One daily task covers all of them.

Each backup is two files in `<dest>\<distro>\`: `<id>.tar.gz` and `<id>.json` (size, SHA-256, warnings,
and the result of the test restore). The id is the UTC time of the backup.

## The test restore

After a backup is written, wslbak imports it with `wsl --import` as a temporary distro, runs a check
inside it, and unregisters it again. The check confirms that the default user and their home
directory exist and that 512 files, picked at random while the backup was being read, have the same
SHA-256 as they had then. Before that, the whole archive is read back and compared with the SHA-256
recorded when it was written.

The temporary distro must not come alive: all WSL2 distros share one network namespace, so a faithful
copy that boots would start a second set of your services and scheduled jobs, with your credentials.
wslbak therefore appends its own `/etc/wsl.conf` to the end of the stream it imports (the archive on
disk is not altered) that turns off systemd, boot commands, Windows drive mounts and interop, and it
refuses to start the copy unless that file is the one that ended up in place.

Only verified backups count towards `--keep`. Backups that could not be verified are kept separately,
at most two, so a run of failures never pushes out your good backups.

## Notifications

A failed or unverified backup raises a Windows notification. `--webhook <url>` adds a second channel:
[ntfy](https://ntfy.sh) topics get a plain-text message, Discord and Slack webhook URLs get their own
JSON format. The URL is treated as a secret: only its host name is shown or logged.

`wslbak status` exits 1 and says so when the last success is more than two days old, when the task is
missing, or when the installed program has disappeared.

## Safeguards

- wslbak never calls `wsl --shutdown`, `wsl --terminate` or `wsl --unregister` on your distros. The one
  place that unregisters anything only accepts a temporary distro whose name wslbak generated, that
  is registered at exactly its own folder under `%LOCALAPPDATA%\wslbak\verify`, and that it left a
  claim file for. A test checks that no other code path can reach that call.
- Old backups are deleted one file at a time, and only files that have a wslbak manifest next to them.
  Other files in the backup folder are left alone, even ones that look like backups.
- `restore` refuses a name that is in use and a folder that is not empty.
- Distros managed by another program (`docker-desktop*`, `rancher-desktop*`, `podman-machine-*`) and
  WSL1 distros are refused with a reason; `wslbak status` lists them before anything is set up.
- Only one wslbak works at a time; a second one exits with code 3.

## How it works

`wsl.exe -d <distro> -u root -e sh -s` runs a small script inside the distro. The script runs GNU `tar`
on `/` with `--one-file-system` and writes the raw archive to stdout. On the Windows side wslbak
compresses it with parallel gzip, hashes it, and writes `<id>.tar.gz.partial`, which is renamed only
when tar reported success and the number of bytes received equals the number tar says it wrote.

The scheduled task belongs to your Windows account and runs with your sign-in session. It starts a
windowless copy of the program kept in `%LOCALAPPDATA%\Programs\wslbak`, so it does not depend on Node
or on anything inside a distro. The task is set to run as soon as possible after a missed start and
again ten minutes after you sign in; the program then skips the run if a backup succeeded within the
last 20 hours.

## Limitations

- **Files that are being written during the backup may be inconsistent in it.** There is no snapshot.
  Databases should be dumped to a file by their own tools; the dump is then backed up reliably.
- **POSIX ACLs are not restored.** They are stored in the archive, but `wsl --import` does not apply
  them. `wslbak run` tells you how many files are affected (systemd's journal folders, which are
  reset at boot, are not counted).
- **Every backup is a full copy.** There is no incremental or deduplicated mode yet.
- **GNU tar is required inside the distro.** Alpine's default BusyBox tar is refused; `apk add tar`.
- **Only what is on the root file system.** A folder mounted from another disk is skipped, and
  `wslbak run` names it.
- **The test restore needs free space** on the drive holding `%LOCALAPPDATA%`, up to about the size of
  the distro's files. When there is not enough, the backup is kept, reported as not verified, and the
  exit code is 1.
- **The task runs only while you are signed in.** A stopped distro is started for the backup.
- **The test restore is a sample.** It proves the archive imports and that 512 files are intact, not
  that every byte of every file is.
- Tested on Windows 11 x64 with WSL 2.7 and Ubuntu and Debian distros. Windows 10, the arm64 build and
  other distros have not been tried yet; reports are welcome.

## Troubleshooting

- **Where is the log?** `%LOCALAPPDATA%\wslbak\wslbak.log`, in English. Add `--debug` to see it live.
- **Windows refuses to start the program.** The executables are not code-signed. Smart App Control
  blocks unsigned programs outright, and wslbak cannot run while it is on; AppLocker or WDAC policies
  can do the same on managed PCs.
- **`status` says the installed program is missing.** Antivirus software may have quarantined it.
  Restore it from the antivirus history, then run `wslbak init` again to put the files back.
- **"Cannot write to …" during init.** With Controlled folder access on, allow the program in Windows
  Security or choose a folder that is not protected.
- **Backups are slow.** If your antivirus scans the archive while it is written, excluding the backup
  folder helps. Do not turn the antivirus off.
- **"cannot run Windows programs" inside WSL.** Windows interop is disabled; check `[interop]` in
  `/etc/wsl.conf`, or install wslbak with Node on Windows instead.

## Development

Requires Go 1.25 or newer and Node.js.

```
npm test         # go vet plus unit tests
npm run build    # builds the four executables in bin/
npm run e2e      # end-to-end tests inside WSL, against a throwaway distro and a sandbox folder
```

`npm run e2e` creates a Debian distro named `wslbak-e2e-<random>` on first use and keeps it for the
next run; `scripts/e2e-distro.sh destroy` removes it. The tests never touch another distro or your
real wslbak settings. When developing inside WSL without Go installed there, the build script falls
back to `go.exe` on Windows; set the `GO` environment variable to point somewhere else.

All interface text lives in `i18n.go`, once per language. Add or change both when you touch a message;
the tests check that nothing is missing.

## License

[MIT](LICENSE)
