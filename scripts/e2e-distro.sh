#!/usr/bin/env bash
# Creates and removes the throwaway WSL distro that the end-to-end tests and the
# experiments run against, so that nothing ever touches a real distro.
#
#   scripts/e2e-distro.sh create    install a distro as wslbak-e2e-<random> and print its name
#   scripts/e2e-distro.sh name      print the name of the current test distro
#   scripts/e2e-distro.sh status    show where it is registered and where it is expected
#   scripts/e2e-distro.sh destroy   unregister it and delete its folder
#
# Run inside WSL. The distro lives in %LOCALAPPDATA%\wslbak-e2e\<name>; its name is
# remembered in local/e2e-distro (local/ is gitignored).
#
# E2E_DISTRO chooses what to install (default: Debian). It is a name from
# `wsl --list --online`, for example FedoraLinux-44, archlinux or openSUSE-Tumbleweed,
# or the word alpine, which downloads Alpine's mini root file system instead (Alpine is
# not in that list). Each choice keeps its own test distro, so several can exist at once.
#
# scripts/e2e.sh sources this file for its helpers.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
E2E_DISTRO="${E2E_DISTRO:-Debian}"
FAMILY="$(printf '%s' "$E2E_DISTRO" | tr 'A-Z' 'a-z' | tr -c 'a-z0-9\n' '-')"
if [ "$FAMILY" = debian ]; then
  STATE="$ROOT/local/e2e-distro"
else
  STATE="$ROOT/local/e2e-distro-$FAMILY"
fi
SYS32="$(wslpath 'C:\Windows\System32')"
WSL="$SYS32/wsl.exe"
REG="$SYS32/reg.exe"
LXSS='HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss'
NAME_RE='^wslbak-e2e-[0-9a-f]{8}$'

# ensure_interop puts back the kernel's handler for Windows programs when it is missing.
#
# All WSL2 distros share one kernel, and with it one list of binfmt_misc handlers. When a
# Fedora 44 distro (systemd 259) shuts down, that list is emptied for everybody, and from
# then on no .exe can be started from any distro ("Exec format error"). The tests start
# and stop faithful copies of the test distro, so on Fedora they trigger this themselves.
# WSL's launcher can still be called directly, which is enough to register the handler
# again as root. This is a workaround for the tests only; wslbak does not do it.
ensure_interop() {
  [ -e /proc/sys/fs/binfmt_misc/WSLInterop ] && return 0
  printf '%s\n' 'systemctl restart systemd-binfmt 2>/dev/null' \
    '[ -e /proc/sys/fs/binfmt_misc/WSLInterop ] || echo ":WSLInterop:M::MZ::/init:P" > /proc/sys/fs/binfmt_misc/register' |
    (cd "$SYS32" && /init "$SYS32/wsl.exe" wsl.exe -d "$WSL_DISTRO_NAME" -u root -e sh -s) >/dev/null 2>&1
  [ -e /proc/sys/fs/binfmt_misc/WSLInterop ]
}

# Windows tools are started from a Windows directory: with a Linux working directory
# cmd.exe and friends print a warning about UNC paths.
win() {
  ensure_interop
  (cd "$SYS32" && "$@")
}

# wsl.exe prints UTF-16 unless WSL_UTF8 is set, and the variable only crosses into
# Windows when it is listed in WSLENV.
wsl_exe() { WSL_UTF8=1 WSLENV="${WSLENV:+$WSLENV:}WSL_UTF8" win "$WSL" "$@"; }

sandbox_win() {
  local base
  base="$(win "$SYS32/cmd.exe" /c 'echo %LOCALAPPDATA%' 2>/dev/null | tr -d '\r')"
  printf '%s\\wslbak-e2e' "$base"
}

reg_value() {
  # stdin is closed on purpose: reg.exe would otherwise swallow the key list that the
  # caller's loop is reading from.
  win "$REG" query "$1" /v "$2" 2>/dev/null </dev/null | tr -d '\r' | awk -F'    ' -v name="$2" '$2 == name { print $NF }'
}

# Prints the registered BasePath of the distro called $1, or nothing when no such distro exists.
registered_path() {
  local key
  while IFS= read -r key; do
    if [ "$(reg_value "$key" DistributionName)" = "$1" ]; then
      reg_value "$key" BasePath | sed 's/^\\\\?\\//'
      return
    fi
  done < <(win "$REG" query "$LXSS" 2>/dev/null | tr -d '\r' | grep -E '\\\{[0-9a-fA-F-]+\}$' || true)
}

current_name() {
  [ -s "$STATE" ] || { echo "no test distro is recorded; run: scripts/e2e-distro.sh create" >&2; exit 1; }
  local name
  name="$(cat "$STATE")"
  [[ "$name" =~ $NAME_RE ]] || { echo "refusing: '$name' in $STATE is not a test distro name" >&2; exit 1; }
  printf '%s\n' "$name"
}

# Alpine is not offered by wsl --install, so its mini root file system is downloaded and
# imported. The file name and checksum come from Alpine's own release list.
import_alpine() {
  local name="$1" dir="$2" base tmp file sum arch
  arch="$(uname -m)"
  base="https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/$arch"
  tmp="$(wslpath -u "$(sandbox_win)")/download-$name"
  mkdir -p "$tmp"
  curl -fsSL "$base/latest-releases.yaml" -o "$tmp/releases.yaml" || return 1
  file="$(awk '/flavor: alpine-minirootfs/{f=1} f && /file:/{print $2; exit}' "$tmp/releases.yaml")"
  sum="$(awk '/flavor: alpine-minirootfs/{f=1} f && /sha256:/{print $2; exit}' "$tmp/releases.yaml")"
  [ -n "$file" ] && [ -n "$sum" ] || { echo "could not find the mini root file system in Alpine's release list" >&2; return 1; }
  curl -fsSL "$base/$file" -o "$tmp/$file" || return 1
  echo "$sum  $tmp/$file" | sha256sum -c - >/dev/null || { echo "checksum mismatch for $file" >&2; return 1; }
  wsl_exe --import "$name" "$dir" "$(wslpath -w "$tmp/$file")" --version 2
  local status=$?
  rm -f "$tmp/$file" "$tmp/releases.yaml"
  rmdir "$tmp" 2>/dev/null
  return $status
}

create() {
  if [ -s "$STATE" ]; then
    echo "a test distro is already recorded: $(cat "$STATE") (run destroy first)" >&2
    exit 1
  fi
  local name dir
  name="wslbak-e2e-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
  dir="$(sandbox_win)\\$name"
  mkdir -p "$ROOT/local"
  # Recorded before installing, so that a half-finished install can still be cleaned up by destroy.
  printf '%s\n' "$name" > "$STATE"
  if [ "$FAMILY" = alpine ]; then
    import_alpine "$name" "$dir" >&2
  else
    wsl_exe --install "$E2E_DISTRO" --name "$name" --location "$dir" --no-launch >&2
  fi
  [ -n "$(registered_path "$name")" ] || { echo "the install did not register $name" >&2; exit 1; }
  printf '%s\n' "$name"
}

# unregister_guarded <name> <expected Windows folder>
# The only place these scripts remove a distro. Three things must hold: the name starts
# with one of the prefixes the tests use, the expected folder is inside the tests' own
# sandbox, and the distro is registered at exactly that folder.
unregister_guarded() {
  local name="$1" expected="$2" actual sandbox dir
  sandbox="$(sandbox_win)"
  [[ "$name" =~ ^wslbak-(e2e|verify)-[0-9A-Za-z-]+$ ]] || { echo "refusing: '$name' is not a test distro name" >&2; return 1; }
  case "${expected,,}" in
    "${sandbox,,}"\\*) ;;
    *) echo "refusing: '$expected' is outside $sandbox" >&2; return 1 ;;
  esac
  actual="$(registered_path "$name")"
  if [ -n "$actual" ]; then
    if [ "${actual,,}" != "${expected,,}" ]; then
      echo "refusing: $name is registered at '$actual', expected '$expected'" >&2
      return 1
    fi
    wsl_exe --unregister "$name" >&2 || return 1
  fi
  dir="$(wslpath -u "$expected")"
  # rmdir only removes an empty folder: unregistering already deleted the virtual disk.
  if [ -d "$dir" ]; then rmdir "$dir" 2>/dev/null || echo "left in place (not empty): $expected" >&2; fi
  return 0
}

destroy() {
  [ -s "$STATE" ] || { echo "no test distro is recorded"; return 0; }
  local name
  name="$(current_name)"
  unregister_guarded "$name" "$(sandbox_win)\\$name" || exit 1
  rm -f "$STATE"
  echo "removed $name"
}

status() {
  local name
  name="$(current_name)"
  echo "name:       $name"
  echo "registered: $(registered_path "$name")"
  echo "expected:   $(sandbox_win)\\$name"
}

# Only act on arguments when run directly, not when sourced.
if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  set -euo pipefail
  case "${1:-}" in
    create) create ;;
    name) current_name ;;
    status) status ;;
    destroy) destroy ;;
    *) sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
  esac
fi
