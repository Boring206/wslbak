# Changelog

## Unreleased

Two safeguards against a run that never ends. Before, such a run meant that backups stopped without
any notification: the stuck run could not report anything, and every later scheduled run found it
still holding the lock and ended quietly.

- A scheduled run that finds another wslbak still running after 20 hours now fails (exit code 2) and
  notifies you that a backup has been running for that many hours and is probably stuck. A run
  started by hand says so too.
- When a WSL command can neither finish nor be ended (which can happen when WSL as a whole stops
  answering), wslbak stops waiting about half a minute after the command's time limit and reports
  the failure, instead of waiting for ever.

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
