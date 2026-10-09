# Changelog

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
- `wslbak list`, `status`, `verify` and `uninstall`; `--dry-run` on every command that changes something.
- A Windows notification when a backup fails or cannot be verified, and optionally a webhook (ntfy,
  Discord, Slack). Notifications never contain file names.
- Interface in English and Traditional Chinese, following the Windows display language; override with
  `--lang` or `WSLBAK_LANG`.
- Distributed as an npm package that installs under Node on Windows and inside WSL, and as a zip that
  needs no Node.
- Tested end to end against Debian, Fedora, Arch Linux, openSUSE Tumbleweed and Alpine.
