# Changelog

## 0.2.1

One correction to backups of a distro in which files are being deleted, and corrections to what
the npm launcher says inside WSL when Windows programs cannot be started.

- A backup failed with "tar reported an error" when a file was deleted in the instant after tar
  had taken it up: tar then cannot look up its access control list and extended attributes by
  name, and says `Warning: Cannot file_has_acl_at: No such file or directory` (or `llistxattrat`).
  The file is in the archive all the same. wslbak took these lines for errors; a file that
  disappears an instant earlier was already accepted as normal on a running system, and so are
  these now. It is rare: of about 150 runs of the tests that create and delete files during a
  backup, one met it. Every earlier version has the same gap.
- The message named `[interop] enabled=false` in `/etc/wsl.conf` as the thing to check when
  Windows programs never worked. That is the cause in a WSL 1 distro only, and the message now says
  so. In a WSL 2 distro that setting does not lead to this message: the kernel setting the launcher
  looks at stays in place, and WSL itself prints an error after about ten seconds (tried with WSL
  2.7.13). The README describes that case in its own entry.
- The command the message prints puts back a setting that is missing in a WSL 2 distro. It does
  not help with one that is switched off, and the message now offers it for a missing one only;
  `wsl --shutdown` restores it in either case.
- When the kernel setting is in place but the program it names cannot be found, the launcher printed
  the same message, which is not true of that state. It now says what it found.

## 0.2.0

Differential backups: a full backup now and then, and in between only what changed since it. They
are off by default; nothing changes for a setup that does not switch them on. Also in this release
are safeguards against a run that never ends, which were prepared as 0.1.2 (that version was never
published), and a number of corrections that an examination of the whole program before this
release turned up.

After updating, run a wslbak command once, for example `wslbak status`: the scheduled task runs a
copy of the program in a fixed place, and that copy is brought up to date when the new version
runs a command (`--version`, `--help` and `--dry-run` do not do it).

- `wslbak config --mode diff` switches a distro to differential backups, `--mode full` back.
  `--full-every <days>` says for how long one full backup is built on (default 30), and
  `wslbak run --full` makes a full backup this once. `wslbak init` mentions the option.
- A differential is `<id>.diff.tar.gz`: the files whose content or attributes changed since the full
  backup, and every directory. `list` has a column for the kind. `restore`, `verify`, `files` and
  `restore --path` take a differential like any other backup and put it together with its full
  backup; a whole distro is still restored with a single `wsl --import`, and folders brought back
  with `restore --path` have their own modification time.
- A differential is only kept when wslbak can account for every file: it is in the differential, or
  it is unchanged in the full backup, in a directory that is still the same directory. When it
  cannot, that run makes a full backup instead and says why. This happens after an old folder was
  renamed, moved or swapped with another, when a folder of the distro is mounted at a second place
  inside it, when the exclude list or the distro's tar changed, when the clock does not fit the
  full backup, and on a root file system other than ext4. It also happens when, while the backup
  runs, a file disappears from a folder the full backup has no record of, or an old folder
  disappears and a new one appears: a new folder cannot be told from an old one that came from a
  place the full backup did not cover, nor a rename from a deletion and a creation; the reason
  given for these is that files or folders came and went while the backup ran. `wslbak status`
  shows whether the last backup was a differential and, if not, the reason.
- A full backup is test-restored after every run, as before. A differential is read back and
  checked after every run, and test-restored together with its full backup for the first
  differential of each full backup and every seven days; `wslbak verify` always does the real
  test restore. With test restores switched off (`--verify none`, or `--no-verify` for one run) a
  differential is not checked at all.
- A full backup is never deleted while a differential that passed its check still needs it.
  Otherwise retention counts every backup, full or differential, as one.
- Full backups are the same archives as before, and 0.1.x can list, test-restore and restore them.
  0.1.x does not show differentials, and deletes nothing in a folder that holds them. It refuses a
  settings file in which differential backups are switched on ("written by a newer version"), until
  `wslbak config --mode full` of this version has switched them off again; the copy of the program
  in the backup folder needs no settings file to restore. Installing 0.1.x again does not by
  itself change the program the scheduled task runs (an older version leaves a newer copy in
  place; `wslbak init` of the older version replaces it), and 0.1.x deletes old backups again only
  once the differentials are gone from the folder. When 0.1.x deletes a full backup that was made
  in differential mode, the small `<id>.dirs.gz` next to it stays behind, and a `--full-every`
  setting is lost the first time 0.1.x writes the settings.
- With differential backups on, a run writes a temporary file inside the distro for each list it
  makes (one to three, one at a time): tar's list of directories and file names, under `/run`, or
  `/dev/shm` where `/run` is not kept in memory, so not on the distro's disk; it is removed when it
  has been read. Without differential backups a backup still writes nothing inside the distro.
- A test restore no longer fails over the distro's host name. WSL writes `/etc/hostname` every time
  it starts a distro, and in the temporary copy it writes the PC's name, because the copy does not
  get the `hostname` setting of the distro's `wsl.conf`. On a distro with a host name of its own the
  file then differed from the backup whenever it was among the files picked for comparison: once in
  a great while for a full backup, and nearly every time for a differential, which compares all of
  its files when there are fewer than 512. It is no longer compared, and where it is a symbolic link
  (NixOS keeps it in `/nix/store`) a difference in the file behind the link is not counted either.
  Found on NixOS, whose `wsl.conf` sets a host name.
- A scheduled run that finds another wslbak still there after 20 hours now fails (exit code 2) and
  notifies you that it has been running for that many hours. A run started by hand says so too.
  This catches a wslbak that was started by hand and never ended, a window left at a question
  included; a run started by the schedule is ended by Windows after 12 hours, without a
  notification. The time this goes by is written into the lock file while a wslbak runs and
  removed when it ends, so that a 0.1.x holding the lock later is not taken for a stuck run.
- A backup that receives nothing from the distro at all is given up after ten minutes, like one
  whose data stops later. Until now the wait for the beginning of the archive had no limit, so a
  WSL that did not answer when the backup started left wslbak waiting until Windows ended the
  task.
- When a WSL command can neither finish nor be ended (which can happen when WSL as a whole stops
  answering), wslbak stops waiting about half a minute after the command's time limit and reports
  the failure, instead of waiting for ever. This includes the backup itself and the listing of
  names, where wslbak now gives up reading from a command that will not end.
- An error that tar reports after more than a thousand harmless messages is no longer overlooked.
  Only the first thousand lines were kept before the run was judged, so a read error behind them
  did not fail the backup.
- Files whose names are not valid UTF-8 are no longer among the files picked for comparison. Such
  a name cannot be written into the backup's record unchanged, so a later `wslbak verify` looked
  for a file that does not exist, failed, and marked a good backup as failed. Records that already
  hold such a name are tolerated.
- With test restores switched off and notifications set to `always`, the notification no longer
  says that the backup passed its test restore. `wslbak status` says "test restore or check" when
  a differential only got the lighter check, and `run --dry-run` says that the differential would
  be read back and checked.
- Inside WSL, when the kernel setting that hands `.exe` files to Windows is missing or switched off
  (another distro that shuts down can remove it), the npm launcher now says so and how to put it back.
  On most distros it printed the shell's complaints about the bytes of the executable instead.
- The message about a settings file from a newer version is now in both languages and says what to
  do, and a scheduled run that cannot load its settings leaves a line in the log.
- The executables in the npm package are marked executable again. In 0.1.1 they were not; inside
  WSL the launcher then sets the mode itself on every run, which it cannot do when the package was
  installed with `sudo`, so it stopped and asked for a `chmod` by hand.
- The README says where to report a problem, with a link, and how to update; `package.json` gives
  the bug tracker in the form that package scanners recognise.
- The Traditional Chinese README is now `README-zh-TW.md`. With two files named `README.*`, npm
  showed one or the other on the package page depending on the machine that published, and the
  page of 0.1.1 came out in Chinese only. It now shows the English README, which links to the
  Chinese one.
- The tour at the top of the README was recorded again with this version, and now shows a
  differential backup as well.
- The README says what was seen with Avast's sandbox (a second, isolated copy of the program that
  sends a failure notification of its own; WSL no longer starting Windows programs), and what was
  found about GitHub's test machines that stopped answering with Microsoft Defender switched on.

## 0.1.1

Four small corrections, found by checking every statement of the README against the code. Backups
and restores work as in 0.1.0.

- wslbak can be installed with Scoop: `scoop bucket add boring206 https://github.com/Boring206/scoop-bucket`,
  then `scoop install wslbak`.
- `wslbak doctor` suggested an exclude pattern for the yarn cache that did not match the folder it
  had measured.
- `README-RESTORE.txt`, which is written into the backup folder, said a backup is two files; it is
  three.
- The usage text now says how `restore` picks a backup when none is verified, and that `off` for
  `--webhook` belongs to `config`.
- Three error messages could show a line that came from inside the distro without making control
  characters visible.

## 0.1.0

First release.

- `wslbak run` backs up a WSL2 distro without stopping it: a root `tar` inside the distro streams the
  root file system out, and the Windows side compresses it into a standard `.tar.gz` that
  `wsl --import` accepts. Ownership, permissions, extended attributes, capabilities, hard links and
  sparse files are preserved.
- Every backup is test-restored: imported as a temporary distro that cannot start services, mount
  Windows drives or use interop, checked against file hashes recorded during the backup, and removed.
- `wslbak init` sets everything up after showing what it will create: a daily scheduled task that needs
  no administrator rights, how many backups to keep, and the destination. `--all` sets up every distro.
- `wslbak restore` restores a backup as a new distro and never overwrites an existing one. A copy of
  the program is kept in the backup folder, so restoring after reinstalling Windows needs nothing else.
- `wslbak files` lists or searches what a backup holds, and `wslbak restore --path … --into …` brings
  back single files or folders into a new folder inside the distro.
- `wslbak config` shows and changes settings: retention (newest, weekly, monthly), excludes, time of
  day, notifications.
- `wslbak doctor` checks WSL, each distro, Windows security settings, antivirus, the schedule,
  destinations and free space, points out large caches that could be excluded, and says when other
  accounts on the PC can read the backup folder (backups are not encrypted).
- `wslbak config --private` restricts the backup folder to your own account.
- `wslbak list`, `status`, `verify` and `uninstall`; `--dry-run` on `init`, `config`, `run`, `restore`
  and `uninstall`.
- A Windows notification when a backup fails or cannot be verified, and optionally a webhook (ntfy,
  Discord, Slack). Notifications never contain file names.
- Interface in English and Traditional Chinese, following the Windows display language; override with
  `--lang` or `WSLBAK_LANG`.
- Distributed as an npm package that installs under Node on Windows and inside WSL, and as a zip that
  needs no Node.
- A file of 8 GiB or more survives a restore. The importer that ships with WSL 2.7 restores such a
  file as empty when an archive is written the way GNU tar writes it by default; wslbak writes the
  size where that importer looks for it, and the test restore checks the sizes of the largest files.
- Tested end to end against Debian, Ubuntu, Fedora, AlmaLinux, Rocky Linux, Oracle Linux, Arch Linux,
  openSUSE Tumbleweed, Kali, Gentoo, NixOS and Alpine, on three builds of Windows; see the README for versions and
  for what has not been tried.
