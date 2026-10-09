# Security

## Reporting a problem

Please report a vulnerability privately, not in a public issue: on the repository page choose
**Security → Report a vulnerability**. Say which version you used (`wslbak --version`), what you did
and what happened. You can expect a first answer within a week.

## What wslbak is trusted with

- It runs `tar` as **root inside your distro**, to read every file. The scripts it runs there are
  part of the program (`backup.sh`, `probe.sh`, `check.sh`, `caches.sh`, `restorefiles.sh`); they use
  only the shell and coreutils, and what you type never reaches a command line inside the distro.
- It registers a scheduled task for **your own Windows account** and needs no administrator rights.
- It can unregister a distro and delete a folder recursively in exactly one place (`fence.go`), and
  only for a temporary distro it created itself. A test checks that no other code can reach that.

## What it protects, and what it does not

| | |
|---|---|
| A backup that silently cannot be restored | Every backup is imported as a temporary distro and checked: 512 sampled files by hash, the largest files by size, the default user. The importer's complaints count as a failure. |
| A temporary distro that "comes alive" | Its settings turn off systemd, boot commands, Windows drives and interop, and wslbak checks that these are the settings that ended up in place before it starts the distro. |
| Overwriting what you have | A restore always goes to a new distro or a new folder. Single files are unpacked where only root can go and then moved into place. |
| Secrets in notifications and logs | The webhook address is shown and logged as its host name only. Notifications never contain file names. |
| Other accounts on the same PC reading your backups | **Not by default.** Backups are not encrypted. `wslbak doctor` tells you when others can read the folder; `wslbak config --private` restricts it. |
| Someone who can write to the backup folder | **Not protected.** They can replace a backup or the copy of the program kept there. A test restore runs programs from the backup inside the WSL virtual machine. Only verify or restore backups from a folder that nobody else can write to. |
| A stolen or lost backup drive | **Not protected.** Use an encrypted drive (BitLocker, for example) if that matters to you. |

## Checking a download

Each release comes with `SHA256SUMS`. For releases built by the release workflow from the public
repository you can also check where a zip was built:

```
gh attestation verify wslbak-<version>-windows-x64.zip --repo Boring206/wslbak
```

The executables are not code-signed.
