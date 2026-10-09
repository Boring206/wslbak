# Helpers shared by the end-to-end scripts. Source it after scripts/e2e-distro.sh, with
# HOME_DIR set to the sandbox folder that wslbak gets through --home.

pass=0
fail=0
skipped=0

# Assertions match the English output, whatever the system language is.
WB=(node bin/wslbak.js --lang en --home "$HOME_DIR")
PS="$SYS32/WindowsPowerShell/v1.0/powershell.exe"

section() { printf '\n\033[1m%s\033[0m\n' "$1"; }
ok() {
	pass=$((pass + 1))
	printf '  \033[32mpass\033[0m %s\n' "$1"
}
bad() {
	fail=$((fail + 1))
	printf '  \033[31mFAIL\033[0m %s\n' "$1"
	[ -n "${2:-}" ] && printf '%s\n' "$2" | sed 's/^/        | /'
}
skip() {
	skipped=$((skipped + 1))
	printf '  \033[33mskip\033[0m %s\n' "$1"
}
# note: something that was measured or observed, not judged.
note() { printf '  \033[36mnote\033[0m %s\n' "$1"; }

# run: run wslbak with stdin at /dev/null; output goes to OUT, exit code to RC, and the
# time it took, in seconds, to TOOK.
run() {
	ensure_interop
	local began=$SECONDS
	OUT="$(timeout "${RUN_LIMIT:-600}" "${WB[@]}" "$@" 2>&1 </dev/null | tr -d '\r'; exit "${PIPESTATUS[0]}")"
	RC=$?
	TOOK=$((SECONDS - began))
}
# run_with NAME=value… -- <arguments>: like run, with environment variables for the Windows
# program (they only cross over from WSL when they are listed in WSLENV).
run_with() {
	local pairs=() names=""
	while [ "$1" != "--" ]; do
		pairs+=("$1")
		names="${names:+$names:}${1%%=*}"
		shift
	done
	shift
	ensure_interop
	OUT="$(env "${pairs[@]}" WSLENV="${WSLENV:+$WSLENV:}$names" timeout "${RUN_LIMIT:-600}" "${WB[@]}" "$@" 2>&1 </dev/null | tr -d '\r'; exit "${PIPESTATUS[0]}")"
	RC=$?
}
expect_rc() {
	if [ "$RC" = "$1" ]; then ok "$2"; else bad "$2 (exit code $RC, expected $1)" "$OUT"; fi
}
expect_has() {
	if grep -qF -- "$1" <<<"$OUT"; then ok "$2"; else bad "$2 (output lacks \"$1\")" "$OUT"; fi
}
expect_lacks() {
	if grep -qF -- "$1" <<<"$OUT"; then bad "$2 (output contains \"$1\")" "$OUT"; else ok "$2"; fi
}
expect_true() {
	local label="$1"
	shift
	if "$@"; then ok "$label"; else bad "$label"; fi
}

# wsl.exe prints its own warnings (lines starting with "wsl: ") next to the command's
# output; they are not part of what the command said.
no_wsl_notes() { tr -d '\r' | grep -v '^wsl: ' || true; }
# On NixOS the usual tools are not on the PATH of a shell started like this.
NIX_PATH_LINE='PATH="$PATH:/run/current-system/sw/bin:/nix/var/nix/profiles/system/sw/bin"; export PATH'
# in_distro <distro> <script file>: run a script as root inside a distro.
in_distro() { { printf '%s\n' "$NIX_PATH_LINE"; cat "$2"; } | win "$WSL" -d "$1" -u root -e sh -s 2>&1 | no_wsl_notes; }
# sh_in <distro> <commands>: run shell commands as root inside a distro. They travel on
# stdin, so nothing has to survive wsl.exe's handling of quotes.
sh_in() { printf '%s\n%s\n' "$NIX_PATH_LINE" "$2" | win "$WSL" -d "$1" -u root -e sh -s 2>&1 | no_wsl_notes; }
# as_default <distro> <commands>: the same as the distro's default user.
as_default() { printf '%s\n%s\n' "$NIX_PATH_LINE" "$2" | win "$WSL" -d "$1" -e sh -s 2>&1 | no_wsl_notes; }
distro_names() { wsl_exe -l -q </dev/null | tr -d '\r' | sed '/^$/d' | sort; }
registered() { [ -n "$(registered_path "$1")" ]; }

finish() {
	printf '\n%d passed, %d failed, %d skipped\n' "$pass" "$fail" "$skipped"
	[ "$fail" -eq 0 ]
}
