# Contributing

Thank you for looking into wslbak. This page says how to build and test it and which rules the
code keeps to. `docs/ARCHITECTURE.md` explains how the parts fit together.

## Building and testing

You need Windows with WSL2, Go 1.26 or newer and Node.js. Everything is run from inside WSL (the
build falls back to `go.exe` on Windows when Go is not installed in the distro).

```
npm test         # go vet and the unit tests
npm run build    # the four executables, and the test helper, in bin/
npm run e2e      # the end-to-end suite against a throwaway distro
```

The end-to-end suite makes real backups and restores. It never touches a distro of yours: it
creates one called `wslbak-e2e-<random>`, keeps everything in `%LOCALAPPDATA%\wslbak-e2e`, and
removes both. See the top of `scripts/e2e.sh` for the switches (`E2E_DISTRO`, `E2E_ROOTFS_URL`,
`E2E_FAST`, `E2E_TOAST`), and `scripts/e2e-scale.sh` and `scripts/e2e-services.sh` for the two
slower scripts.

Before sending a change, please also run what the `lint` job runs: `gofmt -l .`, `staticcheck`,
`govulncheck` and `shellcheck` (the commands are in `.github/workflows/test.yml`).

## Rules the code keeps to

- **One place may destroy things.** Only `fence.go` may unregister a distro or delete a folder
  recursively, and only for wslbak's own temporary distros. `fence_test.go` reads the source to
  make sure.
- **Nothing a user types goes on the `wsl.exe` command line or is interpreted by a shell.** Such
  values travel as shell variables in front of a script that is fed on standard input (`withVars`,
  `shQuote`); a script may hand one on as a single quoted argument (`"--exclude=$pattern"`), never
  as text to be parsed.
- **The scripts that run inside the distro** use only the shell, coreutils and tar (no awk, sed,
  grep, perl, python), never `rm -r`, and end with `main "$@" </dev/null`. A test checks their shape.
- **`--dry-run` creates nothing**, not even a log file.
- **Every message the user sees lives in `i18n.go`**, once in English and once in Traditional
  Chinese. A test checks that neither is missing. Log lines and `--debug` output are English.
- **Comments in the Go code are in Traditional Chinese**; comments in the scripts under
  `scripts/` and in the workflows are in English.
- **Test-only switches** live in `testhooks.go` and only work together with the sandbox option
  `--home`.
- A change that touches backup, restore or the scheduled task should come with an end-to-end
  check, not only a unit test.

## Reporting a bug

Please include the output of `wslbak doctor`, the version (`wslbak --version`), and the relevant
lines of `%LOCALAPPDATA%\wslbak\wslbak.log`. For anything that might be a security problem, see
`SECURITY.md` instead of opening a public issue.
