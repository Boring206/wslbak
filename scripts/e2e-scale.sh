#!/usr/bin/env bash
# Size tests for wslbak: a distro with a very large number of files, and one file bigger
# than the 8 GiB that a classic tar header can describe. Slow and hungry for disk space
# (about three times the size of the big file), so it is not part of npm run e2e.
#
#   bash scripts/e2e-scale.sh [number of files] [size of the big file in GiB]
#
# The defaults are 2,000,000 files and 9 GiB. Like the other end-to-end tests it works on a
# throwaway distro of its own (Alpine with GNU tar) and a sandbox folder, and removes both.
set -u

cd "$(dirname "$0")/.."
FILES="${1:-2000000}"
GIB="${2:-9}"
E2E_DISTRO=alpine
# shellcheck source=scripts/e2e-distro.sh
. scripts/e2e-distro.sh
STATE="$ROOT/local/e2e-distro-scale"

[ -n "${WSL_DISTRO_NAME:-}" ] || { echo "Run this inside WSL."; exit 2; }
[ -f bin/wslbak-x64.exe ] || [ -f bin/wslbak-arm64.exe ] || { echo "The executables are missing; run npm run build first."; exit 2; }

RUN_ID="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
SANDBOX_WIN="$(sandbox_win)\\scale-$RUN_ID"
SANDBOX="$(wslpath -u "$SANDBOX_WIN")"
HOME_DIR="$SANDBOX/home"
DEST="$SANDBOX/dest"
RUN_LIMIT=3600
RESTORED="wslbak-e2e-r-$RUN_ID"
RESTORED_DIR="$SANDBOX_WIN\\restored"
# shellcheck source=scripts/e2e-lib.sh
. scripts/e2e-lib.sh

cleanup() {
	[ -d "$HOME_DIR" ] && timeout 120 "${WB[@]}" uninstall --yes </dev/null >/dev/null 2>&1
	wsl_exe --terminate "$RESTORED" </dev/null >/dev/null 2>&1
	unregister_guarded "$RESTORED" "$RESTORED_DIR" 2>/dev/null
	if wsl_exe -l -q </dev/null | tr -d '\r' | grep -q -e "wslbak-verify-" -e "$RESTORED"; then
		echo "a temporary distro is still registered; leaving $SANDBOX_WIN in place" >&2
	else
		rm -rf "$SANDBOX"
	fi
	[ -z "${KEEP_E2E_DISTRO:-}" ] && [ -s "$STATE" ] && destroy >/dev/null
}
trap cleanup EXIT

# debug_value <label>: the value of a "[debug] label: value" line in OUT.
debug_value() { sed -n "s/^\[debug\] $1: //p" <<<"$OUT" | tail -n 1; }

section "Test distro"
# A distro left over from an earlier run still holds that run's files; start from a fresh one.
[ -s "$STATE" ] && destroy >/dev/null
DISTRO="$(create 2>/dev/null)" || { echo "Could not create the test distro (is there network access?)."; exit 2; }
ok "created $DISTRO"
sh_in "$DISTRO" 'apk add --quiet tar coreutils findutils' >/dev/null
expect_true "it has GNU tar" bash -c "printf 'tar --version\n' | (cd '$SYS32' && ./wsl.exe -d '$DISTRO' -u root -e sh -s) 2>/dev/null | grep -q 'GNU tar'"

section "Planting $FILES files and one file of $GIB GiB"
BEGAN=$SECONDS
PLANTED="$(sh_in "$DISTRO" "
set -e
mkdir -p /scale/files
cd /scale/files
dirs=\$(( ($FILES + 999) / 1000 ))
i=0
while [ \$i -lt \$dirs ]; do
	mkdir -p d\$i
	(cd d\$i && seq 1 1000 | xargs touch)
	i=\$((i + 1))
done
yes 0123456789abcdef | head -c ${GIB}G >/scale/big.bin
sync
echo \"files=\$(find /scale/files -type f | wc -l) big=\$(stat -c %s /scale/big.bin) blocks=\$(stat -c %b /scale/big.bin)\"
" | tail -n 1)"
note "planted in $((SECONDS - BEGAN)) s: $PLANTED"
expect_true "the files are there" grep -q "big=$((GIB * 1024 * 1024 * 1024))" <<<"$PLANTED"
BIG_SUM="$(sh_in "$DISTRO" 'sha256sum /scale/big.bin | cut -d" " -f1')"

section "Backup with a test restore"
run init -d "$DISTRO" --dest "$DEST" --keep 1 --yes
expect_rc 0 "init succeeds"
run --debug run
expect_rc 0 "run succeeds"
expect_has "test restore passed" "and the test restore passes"
note "run took $TOOK s: $(grep -E 'wrote |test restore passed' <<<"$OUT" | sed 's/^ *//' | tr '\n' ';')"
PEAK="$(debug_value 'peak memory')"
note "peak memory of the program during the run: $PEAK"
# The program handles the archive as a stream; its memory must not grow with the distro.
expect_true "the program stays well under a gigabyte of memory" bash -c 'case "$0" in *" MB" | *" KB") exit 0 ;; *) exit 1 ;; esac' "$PEAK"
expect_true "the log stays small" [ "$(stat -c %s "$HOME_DIR/wslbak.log")" -lt 1000000 ]
ID="$(find "$DEST/$DISTRO" -maxdepth 1 -name '*.tar.gz' -printf '%f\n' | sed 's/\.tar\.gz$//' | sort | tail -n 1)"
note "archive $(stat -c %s "$DEST/$DISTRO/$ID.tar.gz" | numfmt --to=iec) for $(grep -o '"size": [0-9]*' "$DEST/$DISTRO/$ID.json" | head -n 1 | cut -d' ' -f2 | numfmt --to=iec) of files; index $(stat -c %s "$DEST/$DISTRO/$ID.idx.gz" | numfmt --to=iec)"

section "Looking inside"
run --debug files /scale/files/d7
expect_rc 0 "files lists a folder among $FILES files"
expect_true "with all 1000 entries" [ "$(grep -c -E '^-' <<<"$OUT")" = 1000 ]
note "files took $TOOK s, peak memory $(debug_value 'peak memory')"
run --debug files --find big.bin
expect_rc 0 "files --find goes through the whole index"
expect_has "/scale/big.bin" "and finds the big file"
expect_has "$GIB.0 GB" "with its full size"
note "files --find took $TOOK s, peak memory $(debug_value 'peak memory')"

section "Restoring the whole distro"
# The real question for a file this size: is it still there after wsl --import? Some versions
# of the importer lose files of 8 GiB or more unless the archive is written with care.
run restore --name "$RESTORED" --to "$SANDBOX/restored" --yes
expect_rc 0 "restore succeeds"
note "restore took $TOOK s"
if registered "$RESTORED"; then
	expect_true "the big file came back whole" [ -n "$BIG_SUM" -a "$(sh_in "$RESTORED" 'sha256sum /scale/big.bin | cut -d" " -f1')" = "$BIG_SUM" ]
	expect_true "and so did every one of the $FILES small files" [ "$(sh_in "$RESTORED" 'find /scale/files -type f | wc -l')" = "$FILES" ]
	wsl_exe --terminate "$RESTORED" </dev/null >/dev/null 2>&1
else
	bad "the restored distro is not registered"
fi
unregister_guarded "$RESTORED" "$RESTORED_DIR" 2>/dev/null

section "Bringing the big file back"
run --debug restore --path /scale/big.bin --into /scale-back --yes
expect_rc 0 "restore --path brings back a file larger than 8 GiB"
note "restore --path took $TOOK s, peak memory $(debug_value 'peak memory')"
expect_true "and it is identical to the original" [ -n "$BIG_SUM" -a "$(sh_in "$DISTRO" 'sha256sum /scale-back/scale/big.bin | cut -d" " -f1')" = "$BIG_SUM" ]
run restore --path /scale/files/d7 --into /scale-back-d7 --yes
expect_rc 0 "restore --path brings back a folder"
expect_true "with all its files" [ "$(sh_in "$DISTRO" 'find /scale-back-d7/scale/files/d7 -type f | wc -l')" = 1000 ]

finish
