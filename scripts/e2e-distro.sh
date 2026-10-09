#!/usr/bin/env bash
# Creates and removes the throwaway WSL distro that the end-to-end tests and the
# experiments run against, so that nothing ever touches a real distro.
#
#   scripts/e2e-distro.sh create    install Debian as wslbak-e2e-<random> and print its name
#   scripts/e2e-distro.sh name      print the name of the current test distro
#   scripts/e2e-distro.sh status    show where it is registered and where it is expected
#   scripts/e2e-distro.sh destroy   unregister it and delete its folder
#
# Run inside WSL. The distro lives in %LOCALAPPDATA%\wslbak-e2e\<name>; its name is
# remembered in local/e2e-distro (local/ is gitignored).
#
# scripts/e2e.sh sources this file for its helpers.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE="$ROOT/local/e2e-distro"
SYS32="$(wslpath 'C:\Windows\System32')"
WSL="$SYS32/wsl.exe"
REG="$SYS32/reg.exe"
LXSS='HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss'
NAME_RE='^wslbak-e2e-[0-9a-f]{8}$'

# Windows tools are started from a Windows directory: with a Linux working directory
# cmd.exe and friends print a warning about UNC paths.
win() { (cd "$SYS32" && "$@"); }

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
  wsl_exe --install Debian --name "$name" --location "$dir" --no-launch >&2
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
