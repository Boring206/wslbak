#!/usr/bin/env bash
# Makes the tour that the README shows, in both languages, by recording it as it happens.
#
#   bash scripts/demo.sh
#
# What it does:
#   - installs a demo distro called wslbak-demo (Debian) and puts a small project in it;
#   - installs the packed npm package into a folder of its own, so that the command typed in
#     the tour really is "wslbak";
#   - runs the commands of the tour for real, without the sandbox option the tests use, and
#     records what they print and when (scripts/demo-record.py);
#   - draws the recording as a terminal (scripts/demo-render.py).
# Nothing in the pictures is edited or replaced.
#
# Because the tour is real it uses the real places: %LOCALAPPDATA%\wslbak, the scheduled task,
# the default backup folder and the default folder for a restored distro. The script refuses
# to start if any of them exists already, and afterwards removes what it created, each thing
# only after checking that it is its own. Do not run it on a PC where wslbak is in use.
#
# It needs, besides what the end-to-end tests need, a Python with Pillow on Windows (set
# DEMO_PYTHON to its path) and, for the .mp4 files, ffmpeg (set FFMPEG).
set -u

cd "$(dirname "$0")/.."
# shellcheck source=scripts/e2e-distro.sh
. scripts/e2e-distro.sh

DEMO=wslbak-demo
[ -n "${WSL_DISTRO_NAME:-}" ] || { echo "Run this inside WSL."; exit 2; }
[ -f bin/wslbak-x64.exe ] || [ -f bin/wslbak-arm64.exe ] || { echo "The executables are missing; run npm run build first."; exit 2; }
PYTHON="${DEMO_PYTHON:-$(wslpath -u "$(win "$SYS32/where.exe" python 2>/dev/null | tr -d '\r' | head -n 1)" 2>/dev/null)}"
[ -x "$PYTHON" ] || { echo "No Python found on Windows; set DEMO_PYTHON."; exit 2; }

env_win() { win "$SYS32/cmd.exe" /c "echo %$1%" 2>/dev/null | tr -d '\r'; }
LOCAL_WIN="$(env_win LOCALAPPDATA)"
PROFILE_WIN="$(env_win USERPROFILE)"
LOCAL="$(wslpath -u "$LOCAL_WIN")"
PROFILE="$(wslpath -u "$PROFILE_WIN")"
WORK="$LOCAL/wslbak-demo-work"
DEMO_DIR_WIN="$LOCAL_WIN\\wslbak-demo-work\\distro"
DEST_ROOT=""
names() { wsl_exe -l -q </dev/null | tr -d '\r' | sed '/^$/d'; }
task_rows() { win "$SYS32/schtasks.exe" /Query /FO CSV /NH </dev/null 2>/dev/null | tr -d '\r' | grep -ci '"\\wslbak-S-'; }

# ---- Refuse to start where wslbak, or an earlier run of this script, has left anything.
for path in "$LOCAL/wslbak" "$LOCAL/Programs/wslbak" "$WORK"; do
	[ -e "$path" ] && { echo "Refusing: $path exists. This script needs a PC where wslbak is not set up."; exit 2; }
done
[ "$(task_rows)" = 0 ] || { echo "Refusing: a wslbak scheduled task exists."; exit 2; }
names | grep -q "^$DEMO" && { echo "Refusing: a distro whose name starts with $DEMO is registered."; exit 2; }
ls -d "$PROFILE/WSL/$DEMO"* >/dev/null 2>&1 && { echo "Refusing: $PROFILE_WIN\\WSL holds a folder of an earlier run."; exit 2; }

# unregister_demo <name> <expected Windows folder>: remove a distro this script made. The
# name must be the demo's or that of its restored copy, and it must be registered at
# exactly the folder this script expects.
unregister_demo() {
	local name="$1" expected="$2" actual
	[[ "$name" =~ ^wslbak-demo(-restored-[0-9]{8})?$ ]] || { echo "refusing: '$name' is not a demo distro" >&2; return 1; }
	actual="$(registered_path "$name")"
	[ -n "$actual" ] || return 0
	if [ "${actual,,}" != "${expected,,}" ]; then
		echo "refusing: $name is registered at '$actual', expected '$expected'" >&2
		return 1
	fi
	wsl_exe --terminate "$name" </dev/null >/dev/null 2>&1
	wsl_exe --unregister "$name" </dev/null >/dev/null 2>&1
	rmdir "$(start_menu)/$name" 2>/dev/null
	rmdir "$(wslpath -u "$expected")" 2>/dev/null
	return 0
}

# remove_install: take down what one recording set up.
remove_install() {
	ensure_interop
	command -v wslbak >/dev/null 2>&1 && [ -d "$LOCAL/wslbak" ] && WSLBAK_LANG=en timeout 120 wslbak uninstall --yes </dev/null >/dev/null 2>&1
	local name entry extra
	for name in $(names | grep -E "^$DEMO-restored-[0-9]{8}$"); do
		unregister_demo "$name" "$PROFILE_WIN\\WSL\\$name"
	done
	rmdir "$PROFILE/WSL" 2>/dev/null
	# The backup folder: only if it holds nothing but what wslbak puts there for the demo.
	if [ -n "$DEST_ROOT" ] && [ -d "$DEST_ROOT" ]; then
		extra=""
		for entry in "$DEST_ROOT"/* "$DEST_ROOT"/.[!.]*; do
			[ -e "$entry" ] || continue
			case "$(basename "$entry")" in "$DEMO" | wslbak.exe | README-RESTORE.txt) ;; *) extra="$entry" ;; esac
		done
		if [ -z "$extra" ]; then rm -rf "$DEST_ROOT"; else echo "left in place (it holds other things): $DEST_ROOT" >&2; fi
	fi
	# Settings and log of the demo's installation. Only config.json, state.json, the log, the
	# lock and the folder for test restores can be in there.
	if [ -d "$LOCAL/wslbak" ] && [ -z "$(names | grep '^wslbak-verify-')" ]; then rm -rf "$LOCAL/wslbak"; fi
}

cleanup() {
	remove_install
	unregister_demo "$DEMO" "$DEMO_DIR_WIN"
	if names | grep -q "^$DEMO"; then
		echo "a demo distro is still registered; leaving $WORK in place" >&2
	else
		rm -rf "$WORK"
	fi
}
trap cleanup EXIT

# ---- The command "wslbak": the package as npm would install it.
mkdir -p "$WORK" local/demo docs
TGZ="$(npm pack --ignore-scripts --silent --pack-destination "$WORK" 2>/dev/null | tail -n 1)"
npm install --global --prefix "$WORK/npm" --silent --no-audit --no-fund "$WORK/$TGZ" >/dev/null 2>&1 || { echo "Could not install the packed package."; exit 2; }
export PATH="$WORK/npm/bin:$PATH"
command -v wslbak >/dev/null || { echo "wslbak is not on the PATH after installing it."; exit 2; }

# ---- The demo distro and the small project in it.
ensure_interop
wsl_exe --install Debian --name "$DEMO" --location "$DEMO_DIR_WIN" --no-launch </dev/null >/dev/null 2>&1
[ -n "$(registered_path "$DEMO")" ] || { echo "Could not install the demo distro."; exit 2; }
in_demo() { printf '%s\n' "$1" | win "$WSL" -d "$DEMO" -u root -e sh -s 2>&1 | tr -d '\r' | grep -v '^wsl: ' || true; }
in_demo 'useradd -m -u 1000 -s /bin/bash me' >/dev/null
wsl_exe --manage "$DEMO" --set-default-user me </dev/null >/dev/null 2>&1
reset_project() {
	in_demo '
rm -rf /home/me/recovered /home/me/project
mkdir -p /home/me/project/src /home/me/project/data
printf "# Release notes\n\n- ship wslbak 0.1.0\n- write the README\n" >/home/me/project/notes.md
printf "print(\"hello\")\n" >/home/me/project/src/app.py
printf "id,name\n1,alpha\n2,beta\n" >/home/me/project/data/items.csv
printf "node_modules/\n" >/home/me/project/.gitignore
chown -R me:me /home/me' >/dev/null
}

# ---- Where will the backups go? Ask wslbak, and refuse if that folder is already there.
PLAN="$(WSLBAK_LANG=en timeout 120 wslbak init -d "$DEMO" --dry-run </dev/null 2>&1 | tr -d '\r')"
DEST_WIN="$(sed -n 's/^ *Backups go to *//p' <<<"$PLAN" | head -n 1)"
[ -n "$DEST_WIN" ] || { echo "Could not find out where the backups would go:"; echo "$PLAN"; exit 2; }
DEST_ROOT="$(dirname "$(wslpath -u "$DEST_WIN")")"
if [ -e "$DEST_ROOT" ]; then
	echo "Refusing: the default backup folder $(wslpath -w "$DEST_ROOT") exists already."
	DEST_ROOT=""
	exit 2
fi

status=0
for lang in en zh-TW; do
	reset_project
	ensure_interop
	rm -f "local/demo/$lang.json"
	WSLBAK_LANG="$lang" DEMO_SETTINGS="$LOCAL/wslbak/config.json" python3 scripts/demo-record.py "$lang" "$DEMO" "local/demo/$lang.json" || status=1
	remove_install
done
[ "$status" = 0 ] || { echo "A step of the tour failed; nothing was drawn."; exit 1; }

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
