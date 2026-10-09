#!/usr/bin/env bash
# Makes the demo that the README shows, in both languages:
#   1. records real wslbak commands against a throwaway distro (scripts/demo-record.py),
#   2. draws the recording as an animation of a terminal (scripts/demo-render.py).
#
#   bash scripts/demo.sh
#
# Besides what the end-to-end tests need, this needs a Python with Pillow on Windows (set
# DEMO_PYTHON to its path) and, for the .mp4 files, ffmpeg (set FFMPEG; without it only the
# .gif files are made). DEMO_DEST_ROOT chooses where the demo's backups go while it runs,
# for example /mnt/d: a folder on another drive than the distro keeps a warning about that
# out of the picture. Everything the demo creates is removed again.
set -u

cd "$(dirname "$0")/.."
# shellcheck source=scripts/e2e-distro.sh
. scripts/e2e-distro.sh
STATE="$ROOT/local/e2e-distro-demo"
HOME_DIR=/nonexistent
# shellcheck source=scripts/e2e-lib.sh
. scripts/e2e-lib.sh

[ -n "${WSL_DISTRO_NAME:-}" ] || { echo "Run this inside WSL."; exit 2; }
[ -f bin/wslbak-x64.exe ] || [ -f bin/wslbak-arm64.exe ] || { echo "The executables are missing; run npm run build first."; exit 2; }
PYTHON="${DEMO_PYTHON:-$(wslpath -u "$(win "$SYS32/where.exe" python 2>/dev/null | tr -d '\r' | head -n 1)" 2>/dev/null)}"
[ -x "$PYTHON" ] || { echo "No Python found on Windows; set DEMO_PYTHON."; exit 2; }

RUN_ID="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
SANDBOX="$(wslpath -u "$(sandbox_win)")/demo-$RUN_ID"
DEST="${DEMO_DEST_ROOT:-$SANDBOX}/wslbak-demo-$RUN_ID/WSLBackup"
mkdir -p local/demo docs

if [ -s "$STATE" ] && registered "$(current_name)"; then
	DISTRO="$(current_name)"
else
	rm -f "$STATE"
	DISTRO="$(create 2>/dev/null)" || { echo "Could not create the demo distro."; exit 2; }
	sh_in "$DISTRO" 'useradd -m -u 1000 -s /bin/bash me' >/dev/null
	wsl_exe --manage "$DISTRO" --set-default-user me </dev/null >/dev/null 2>&1
fi

# The small project that the demo backs up, deletes a file from and gets it back for.
reset_project() {
	sh_in "$DISTRO" '
rm -rf /home/me/recovered /home/me/project
mkdir -p /home/me/project/src /home/me/project/data
printf "# Release notes\n\n- ship wslbak 0.1.0\n- write the README\n" >/home/me/project/notes.md
printf "print(\"hello\")\n" >/home/me/project/src/app.py
printf "id,name\n1,alpha\n2,beta\n" >/home/me/project/data/items.csv
printf "node_modules/\n" >/home/me/project/.gitignore
chown -R me:me /home/me' >/dev/null
}

# remove_run <home>: take down what one recording set up.
remove_run() {
	[ -d "$1" ] && timeout 120 node bin/wslbak.js --lang en --home "$1" uninstall --yes </dev/null >/dev/null 2>&1
	local name
	for name in $(wsl_exe -l -q </dev/null | tr -d '\r' | grep "^$DISTRO-restored-"); do
		wsl_exe --terminate "$name" </dev/null >/dev/null 2>&1
		unregister_guarded "$name" "$(registered_path "$name")" 2>/dev/null
	done
}

cleanup() {
	for lang in en zh-TW; do remove_run "$SANDBOX/$lang/home"; done
	if wsl_exe -l -q </dev/null | tr -d '\r' | grep -q -e "^$DISTRO-restored-" -e "^wslbak-verify-"; then
		echo "a distro of this run is still registered; leaving $SANDBOX in place" >&2
	else
		rm -rf "$SANDBOX" "${DEMO_DEST_ROOT:-$SANDBOX}/wslbak-demo-$RUN_ID"
	fi
	[ -z "${KEEP_E2E_DISTRO:-}" ] && [ -s "$STATE" ] && destroy >/dev/null
}
trap cleanup EXIT

status=0
for lang in en zh-TW; do
	reset_project
	ensure_interop
	python3 scripts/demo-record.py "$lang" "$DISTRO" "$SANDBOX/$lang/home" "$DEST-$lang" "$SANDBOX/$lang/restored" "local/demo/$lang.json" || status=1
	remove_run "$SANDBOX/$lang/home"
done
[ "$status" = 0 ] || { echo "A step of the demo failed; nothing was drawn."; exit 1; }

for lang in en zh-TW; do
	ensure_interop
	render() {
		win "$PYTHON" "$(wslpath -w "$ROOT/scripts/demo-render.py")" "$(wslpath -w "$ROOT/local/demo/$lang.json")" \
			"$(wslpath -w "$ROOT/docs")\\demo.$lang" "$@" | tr -d '\r'
	}
	# The picture for the README, and a larger one as a video.
	render --width 960
	[ -n "${FFMPEG:-}" ] && render --width 1280 --ffmpeg "$FFMPEG" --mp4-only
done
