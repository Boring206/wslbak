# How wslbak is put together

One Go program, built for Windows, in a single package. It is shipped through npm with a small
launcher (`bin/wslbak.js`) that picks the executable for the machine and, inside WSL, converts
Linux paths to Windows ones. There are two builds of the same program: `wslbak.exe` for the
terminal and `wslbakw.exe` without a console window, for the scheduled task.

## A backup, step by step

```
Windows                                             inside the distro
-------                                             -----------------
wslbak run
  wsl.exe -d <distro> -u root -e sh -s   --------►  backup.sh (on standard input)
                                                      GNU tar --create / … --one-file-system
  tar's bytes on standard output         ◄--------    (standard error: tar's messages, and lines
                                                       starting with @wslbak for wslbak itself)
  sizeFixReader   fills in the size of files of 8 GiB or more        (tarfix.go)
  scanTar         writes the file index, hashes 512 sample files,
                  notes the largest files and where the archive ends  (tarscan.go, index.go)
  pgzip + SHA-256 → <id>.tar.gz.partial                               (backup.go)
  rename to <id>.tar.gz only if tar reported success and the byte
  counts agree; then <id>.json (the manifest) and <id>.idx.gz
```

`wsl --export` is not used, because it stops the distro first.

## The test restore

```
<id>.tar.gz ─► gunzip ─► overrideReader ─► wsl --import <temporary name> <folder> -
                              │
                              └─ appends /etc/wsl.conf and /etc/wsl-distribution.conf that switch
                                 off systemd, boot commands, Windows drives and interop
            (the same bytes also go through scanOverride, which confirms that the appended
             files are the ones that end up in place; if not, the distro is never started)
check.sh runs in the temporary distro: is it inert, does the default user exist, do the sampled
files have their recorded hashes, do the largest files have their recorded sizes
fence.go unregisters the temporary distro and deletes its folder
```

## Restoring

- A whole distro: `wsl --import <new name> <folder> <id>.tar.gz`, then the default user is set
  back. An existing name or a non-empty folder is refused.
- Single files (`restore --path … --into …`): `walkTar` passes the chosen entries through
  unchanged, GNU tar inside the distro unpacks them into `/.wslbak-restore-<random>` (root only),
  and `restorefiles.sh` moves that folder to the target with `mv -T`.

## The files

| File | What it holds |
|---|---|
| `main.go` | Arguments, which command, exit codes |
| `i18n.go` | Every message the user sees, in English and Traditional Chinese |
| `config.go`, `configcmd.go` | The settings file; `config`, including `--private` |
| `install.go` | `init`, `uninstall`, the fixed copy of the program, choosing distros and the destination |
| `schedule.go` | The scheduled task: its XML, creating, querying, deleting |
| `wsl.go`, `wslreg.go`, `wslapi.go` | Starting `wsl.exe`, reading the list of distros from the registry, setting the default user |
| `backup.go`, `backup.sh` | One backup |
| `tarfix.go` | The size of files of 8 GiB or more, written where WSL's importer looks for it |
| `tarscan.go` | Reading the tar stream: samples, largest files, the appended settings and their check |
| `index.go`, `files.go` | The file index; `files` |
| `tarwalk.go`, `restorepath.go`, `restorefiles.sh` | Passing chosen entries through unchanged; single-file restore |
| `verify.go`, `check.sh` | The test restore |
| `fence.go` | The only place that removes a distro or deletes a folder recursively |
| `restore.go` | Restoring a whole distro |
| `manifest.go`, `retention.go` | The record kept next to each backup; which backups to keep |
| `run.go` | `run`, `list`, `status`, `verify`; when to notify |
| `notify.go` | The Windows notification and the webhook |
| `doctor.go`, `probe.sh`, `caches.sh` | `doctor`; what a distro looks like; how big its caches are |
| `disk.go`, `paths.go`, `lock.go`, `log.go`, `ui.go` | Volumes, folders, the single-instance lock, the log, the terminal |
| `testhooks.go` | Switches for the tests; they only work together with `--home` |
| `bin/wslbak.js` | The npm launcher |
| `scripts/` | Building, packaging, the end-to-end suites, the demo |

## Where things live on the PC

| | |
|---|---|
| Settings, state, log | `%LOCALAPPDATA%\wslbak\` (`config.json`, `state.json`, `wslbak.log`) |
| The program the task runs | `%LOCALAPPDATA%\Programs\wslbak\` |
| Temporary distros of test restores | `%LOCALAPPDATA%\wslbak\verify\` |
| Backups | `<destination>\<distro>\<id>.tar.gz`, `.json`, `.idx.gz` |
| A copy of the program for restoring | `<destination>\wslbak.exe`, `README-RESTORE.txt` |

With `--home <folder>` all of the first three move into that folder and the task gets another
name; the tests use this as their sandbox.

## Tests

- Unit tests (`*_test.go`) cover the parts that can be decided without WSL: parsing, the tar
  readers, retention, the task's XML, messages, and the rules above about destructive calls and
  script shape.
- `scripts/e2e.sh` runs the real program against a throwaway distro. It is the only way to test
  backup, import, the scheduled task and the console. `.github/workflows/e2e.yml` runs it on
  Windows Server 2022 and 2025, once with Microsoft Defender's real-time protection on.
- `scripts/e2e-scale.sh` (two million files, a 9 GiB file) and `scripts/e2e-services.sh` (Docker
  and databases at work) are run by hand.
