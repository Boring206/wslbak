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

## A differential backup

Off unless `config --mode diff` switched it on. A differential holds what changed since one full
backup, and is only kept when it can be shown that the full backup and the differential together
are the tree as it is now. Otherwise a full backup is made instead (`diff.go`).

```
1  the listing    backup.sh in list mode: GNU tar --create --file=/dev/null
                  --listed-incremental=<file under /run>. tar opens no file; the snapshot file
                  it leaves names every directory with its inode and the names in it. It is
                  sent out on standard output and deleted.              (snapshot.go)
2  the dump       backup.sh in diff mode: the same tar options as a full backup, plus
                  --after-date=@<since>: every directory, and the files whose content or
                  attributes changed since the full backup → <id>.diff.tar.gz
3  the proof      every name in the listing must be in that dump, or be in the full backup
                  at the same path, in a directory with the same inode as at the time of
                  the full backup. If so: one bit per entry of the full backup saying whether
                  this restore point still wants it, kept in the manifest.  (diff.go, explainer)
```

A full backup made in this mode is the same archive as ever, with two extras: the listing is
taken before and after the dump, and the directories that had the same inode both times are
written to `<id>.dirs.gz`; and the manifest records the time later differentials ask tar about
(the earlier of the distro's clock and Windows' clock, less five minutes).

Older versions and these files: a full backup keeps manifest format 1, and 0.1.x reads it (it
ignores the fields it does not know, and drops them if it writes the manifest again, after which
that full backup can no longer be built on and the next run makes a new one). A differential is
format 2, which 0.1.x neither shows nor deletes; seeing one, it deletes nothing in that folder.
The settings file is format 2 only while a distro has differential backups on, and 0.1.x refuses
that file.

`--listed-incremental` is used for the listing only. Archives are never written in tar's
incremental format: restoring it means deleting and renaming inside a distro, and its headers
are larger than the tar reader wslbak checks its archives with accepts.

What makes a full backup be made instead: no full backup to build on, or one that failed its
test restore; another exclude list or tar; a root file system other than ext4; a folder mounted
at a second place; the clock earlier than at the full backup, or a directory whose change time
is earlier than the full backup and yet not the one recorded; the full backup older than
`--full-every`; the last differential larger than half the full backup, or failed; a listing
that is not one tree; a directory that is not the one it was (renamed, moved or swapped) with
unchanged files in it; a hard link whose target would be dropped.

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

Files that WSL writes whenever it starts a distro are not sampled (`volatilePaths` in tarscan.go:
the host name, hosts, resolv.conf and ld.so.cache, and the two settings files that are replaced in
the copy). The copy starts without the distro's own `wsl.conf`, so WSL writes the PC's name into
its `/etc/hostname`, not the name the distro asked for. Where that path is a symbolic link (NixOS
keeps the file in `/nix/store`), it is the file behind the link that changes, and only the copy can
tell which file that is. check.sh therefore resolves `/etc/hostname`, `/etc/hosts` and
`/etc/resolv.conf` in the copy and reports a sampled file that differs there as skipped, not as
bad; `judgeCheck` accepts at most three of those. A differential samples every file it holds when
there are fewer than 512, and the host name file is in it whenever the distro was started since the
full backup, so without this every such test restore of a distro with its own host name failed.

A differential restore point is test-restored as it would be restored: merged with its full
backup into one stream (see below), then the same appended settings and the same check, with the
samples of both. That happens for the first differential of a full backup and when the last such
test is seven days old; the others get the light check (`runCheck`): the differential is read
back, its checksum, structure and entry count compared, and its full backup is still there with
the size it had.

## Restoring

- A whole distro: `wsl --import <new name> <folder> <id>.tar.gz`, then the default user is set
  back. An existing name or a non-empty folder is refused.
- Single files (`restore --path … --into …`): `walkTar` passes the chosen entries through
  unchanged, GNU tar inside the distro unpacks them into `/.wslbak-restore-<random>` (root only),
  and `restorefiles.sh` moves that folder to the target with `mv -T`.
- A differential restore point, for either of the above and for `files`: `mergeTar` writes the
  differential's directories, then the entries of the full backup that the manifest's bits do not
  drop, then the differential's other entries. Every path appears once and directories come
  first, so nothing depends on how an importer treats an entry it has seen before. Entries are
  passed through byte for byte; both archives are read to the end and compared with their
  checksums. For a whole distro the stream goes to `wsl --import <new name> <folder> -`.

## The files

| File | What it holds |
|---|---|
| `main.go` | Arguments, which command, exit codes |
| `i18n.go` | Every message the user sees, in English and Traditional Chinese |
| `config.go`, `configcmd.go` | The settings file; `config`, including `--private` |
| `install.go` | `init`, `uninstall`, the fixed copy of the program, choosing distros and the destination |
| `schedule.go` | The scheduled task: its XML, creating, querying, deleting |
| `wsl.go`, `wslreg.go`, `wslapi.go` | Starting `wsl.exe`, reading the list of distros from the registry, setting the default user |
| `backup.go`, `backup.sh` | One backup: a tar run and what is checked about it; the listing pass; full and differential |
| `snapshot.go` | Reading tar's snapshot file into a listing, and checking that it is one tree |
| `diff.go` | Full or differential, and why; the proof; the record of directories; when to test-restore |
| `archive.go` | Reading archives back with their checksum; writing a merged restore point; the light check |
| `tarfix.go` | The size of files of 8 GiB or more, written where WSL's importer looks for it |
| `tarscan.go` | Reading the tar stream: samples, largest files, the appended settings and their check |
| `index.go`, `files.go` | The file index; `files` |
| `tarwalk.go`, `restorepath.go`, `restorefiles.sh` | Passing chosen entries through unchanged; merging a full backup and a differential; single-file restore |
| `verify.go`, `check.sh` | The test restore |
| `fence.go` | The only place that removes a distro or deletes a folder recursively |
| `restore.go` | Restoring a whole distro |
| `manifest.go`, `retention.go` | The record kept next to each backup; which backups to keep |
| `run.go` | `run`, `list`, `status`, `verify`; when to notify |
| `notify.go` | The Windows notification and the webhook |
| `doctor.go`, `probe.sh`, `caches.sh` | `doctor`; what a distro looks like; how big its caches are |
| `disk.go`, `paths.go`, `lock.go`, `log.go`, `ui.go` | Volumes, folders, the single-instance lock (which records when its holder started, so that a run still there 20 hours later is reported), the log, the terminal |
| `testhooks.go` | Switches for the tests; they only work together with `--home` |
| `bin/wslbak.js` | The npm launcher |
| `scripts/` | Building, packaging, the end-to-end suites, the demo |

## Where things live on the PC

| | |
|---|---|
| Settings, state, log | `%LOCALAPPDATA%\wslbak\` (`config.json`, `state.json`, `wslbak.log`) |
| The program the task runs | `%LOCALAPPDATA%\Programs\wslbak\` |
| Temporary distros of test restores | `%LOCALAPPDATA%\wslbak\verify\` |
| Backups | `<destination>\<distro>\<id>.tar.gz`, `.json`, `.idx.gz`; with differential backups also `<id>.diff.tar.gz` and, next to a full backup, `<id>.dirs.gz` |
| A copy of the program for restoring | `<destination>\wslbak.exe`, `README-RESTORE.txt` |

With `--home <folder>` all of the first three move into that folder and the task gets another
name; the tests use this as their sandbox.

## Tests

- Unit tests (`*_test.go`) cover the parts that can be decided without WSL: parsing, the tar
  readers, retention, the task's XML, messages, and the rules above about destructive calls and
  script shape.
- `scripts/e2e.sh` runs the real program against a throwaway distro. It is the only way to test
  backup, import, the scheduled task and the console. `.github/workflows/e2e.yml` runs it on
  Windows Server 2022 and 2025, once with Microsoft Defender's real-time protection on. That
  last machine freezes as a whole now and then (5 of 65 runs); this was looked into and is not
  wslbak's doing, see "Where it has been tested" in the README.
- `scripts/e2e-scale.sh` (two million files, a 9 GiB file) and `scripts/e2e-services.sh` (Docker
  and databases at work) are run by hand.
- `scripts/e2e-tarfacts.sh`, also run by hand, checks what differential backups take for granted
  about programs wslbak does not ship: what GNU tar's listing pass and `--after-date` do, what
  changes an inode's change time on ext4, and what `wsl --import` does with a stream that names a
  path twice. Run it again when a new GNU tar or WSL appears.
- `TestRealDifferential` is the unit test to look at first when a differential goes wrong: it
  merges a full backup and a differential written by real GNU tar (`scripts/mkfixtures.sh`) and
  compares the result with a second full backup of the changed tree.
