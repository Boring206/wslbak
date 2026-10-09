#!/usr/bin/env bash
# End-to-end tests for wslbak. Run inside WSL:
#   npm run build && npm run e2e
#
# Nothing here touches a real distro or the real wslbak settings. Everything runs against
# a throwaway Debian distro (wslbak-e2e-<random>, see scripts/e2e-distro.sh) and a sandbox
# folder passed with --home, under %LOCALAPPDATA%\wslbak-e2e. The test distro is created
# on first use and kept for the next run; remove it with: scripts/e2e-distro.sh destroy
set -u

cd "$(dirname "$0")/.."
# shellcheck source=scripts/e2e-distro.sh
. scripts/e2e-distro.sh

[ -n "${WSL_DISTRO_NAME:-}" ] || { echo "Run this inside WSL."; exit 2; }
[ -f bin/wslbak-x64.exe ] || [ -f bin/wslbak-arm64.exe ] || { echo "The executables are missing; run npm run build first."; exit 2; }

pass=0
fail=0
skipped=0
RUN_ID="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
SANDBOX_WIN="$(sandbox_win)\\run-$RUN_ID"
SANDBOX="$(wslpath -u "$SANDBOX_WIN")"
HOME_DIR="$SANDBOX/home"
DEST="$SANDBOX/dest"
# Distros the tests create besides the long-lived test distro: "name|Windows folder".
extra_distros=()

# Assertions below match the English output, whatever the system language is.
WB=(node bin/wslbak.js --lang en --home "$HOME_DIR")

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

# run: run wslbak with stdin at /dev/null; output goes to OUT, exit code to RC.
run() {
	OUT="$(timeout 600 "${WB[@]}" "$@" 2>&1 </dev/null | tr -d '\r'; exit "${PIPESTATUS[0]}")"
	RC=$?
}
# run_kit: the same for the copy of the program that sits in the backup folder.
run_kit() {
	OUT="$(timeout 600 "$DEST/wslbak.exe" --lang en --home "$(wslpath -w "$SANDBOX/empty-home")" "$@" 2>&1 </dev/null | tr -d '\r'; exit "${PIPESTATUS[0]}")"
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

# in_distro <distro> <script file>: run a script as root inside a distro.
in_distro() { win "$WSL" -d "$1" -u root -e sh -s <"$2" 2>&1 | tr -d '\r'; }
# sh_in <distro> <commands>: run shell commands as root inside a distro. They travel on
# stdin, so nothing has to survive wsl.exe's handling of quotes.
sh_in() { printf '%s\n' "$2" | win "$WSL" -d "$1" -u root -e sh -s 2>&1 | tr -d '\r'; }
distro_names() { wsl_exe -l -q </dev/null | tr -d '\r' | sed '/^$/d' | sort; }
registered() { [ -n "$(registered_path "$1")" ]; }
task_name() {
	local sid
	sid="$(win "$SYS32/whoami.exe" /user /fo csv /nh </dev/null | tr -d '\r' | sed 's/.*,"\(S-[0-9-]*\)".*/\1/')"
	printf 'wslbak-sandbox-%s' "$sid"
}
task_exists() { win "$SYS32/schtasks.exe" /Query /TN "$(task_name)" </dev/null >/dev/null 2>&1; }
task_absent() { ! task_exists; }
# When PID 1 of a distro started: it changes if the distro is stopped and started again.
instance_of() { sh_in "$1" 'cut -d" " -f22 /proc/1/stat'; }
backups() { find "$DEST/$DISTRO" -maxdepth 1 -name '*.tar.gz' -printf '%f\n' 2>/dev/null | sed 's/\.tar\.gz$//' | sort; }
newest_backup() { backups | grep -E '^[0-9]{8}T[0-9]{6}Z$' | tail -n 1; }
# Everything in the sandbox, the list of distros and whether the task exists, as one text.
snapshot() {
	(cd "$SANDBOX" 2>/dev/null && find . -printf '%p %s\n' | sort)
	distro_names
	task_exists && echo "task: present" || echo "task: absent"
}

cleanup() {
	# Let wslbak remove its own task and temporary distros first.
	if [ -d "$HOME_DIR" ]; then
		timeout 120 "${WB[@]}" uninstall --yes </dev/null >/dev/null 2>&1
	fi
	task_exists && win "$SYS32/schtasks.exe" /Delete /TN "$(task_name)" /F </dev/null >/dev/null 2>&1
	local entry
	for entry in "${extra_distros[@]:-}"; do
		[ -n "$entry" ] && unregister_guarded "${entry%%|*}" "${entry#*|}" 2>/dev/null
	done
	# The sandbox holds only files now. Deleting it is safe as long as no distro is still
	# registered inside it.
	if wsl_exe -l -q </dev/null | tr -d '\r' | grep -q "wslbak-.*$RUN_ID"; then
		echo "a test distro of this run is still registered; leaving $SANDBOX_WIN in place" >&2
	else
		rm -rf "$SANDBOX"
	fi
	[ "${created_distro:-0}" = 1 ] && [ -z "${KEEP_E2E_DISTRO:-}" ] && destroy >/dev/null
}
trap cleanup EXIT

section "0. Test distro"
created_distro=0
if DISTRO="$(current_name 2>/dev/null)" && registered "$DISTRO"; then
	ok "reusing $DISTRO"
else
	rm -f "$STATE"
	if DISTRO="$(create 2>/dev/null)"; then
		created_distro=1
		ok "created $DISTRO"
	else
		echo "Could not create the test distro (is there network access for wsl --install?)."
		exit 2
	fi
fi
SEEDED="$(in_distro "$DISTRO" scripts/e2e-seed.sh | tail -n 1)"
if [ "$SEEDED" = seeded ]; then ok "planted the test files"; else bad "planting the test files" "$SEEDED"; fi
BEFORE_DISTROS="$(distro_names)"

section "1. Nothing is set up yet"
run status
expect_rc 1 "status exits 1 when nothing is set up"
expect_has "not set up yet" "status says so"
run run
expect_rc 2 "run refuses without a configuration"
run list
expect_rc 2 "list refuses without a configuration"
expect_true "none of that created a settings file" [ ! -e "$HOME_DIR/config.json" ]

section "2. --dry-run changes nothing"
BEFORE="$(snapshot)"
run init -d "$DISTRO" --dest "$DEST" --keep 2 --at 4:30 --dry-run
expect_rc 0 "init --dry-run succeeds"
expect_has "About to set up" "it shows the plan"
expect_has "wslbak-sandbox-S-1-5-" "the plan names the scheduled task"
expect_has "nothing above was actually done" "it says nothing was done"
expect_true "init --dry-run left everything as it was" [ "$(snapshot)" = "$BEFORE" ]

section "3. init"
run init -d "$DISTRO" --dest "$DEST" --keep 2 --at 4:30 --yes
expect_rc 0 "init succeeds"
expect_true "the settings file exists" [ -f "$HOME_DIR/config.json" ]
expect_true "the program was copied to a fixed place" [ -f "$HOME_DIR/program/wslbakw.exe" ]
expect_true "the scheduled task exists" task_exists
expect_true "the restore kit is in the destination" [ -f "$DEST/wslbak.exe" ]
expect_true "with its instructions" [ -f "$DEST/README-RESTORE.txt" ]
run status
expect_has "every day at 04:30" "status shows the schedule"
expect_has "No backup has succeeded yet" "status says there is no backup yet"
expect_rc 1 "status exits 1 until a backup has succeeded"
run init -d no-such-distro --yes
expect_rc 2 "init refuses a distro that does not exist"
run init -d "$DISTRO" --dest '\\wsl.localhost\'"$DISTRO"'\backup' --yes
expect_rc 2 "init refuses a destination inside WSL"
expect_has "inside a WSL file system" "and explains why"

section "4. Backup with a test restore"
# Keep the distro busy while it is being read: a backup of a running system must cope.
sh_in "$DISTRO" '
mkdir -p /root/churn
cat > /root/churn.sh <<"CHURN"
i=0
while [ $i -lt 6000 ]; do
	echo $i > /root/churn/f$((i % 40))
	rm -f /root/churn/f$(((i + 20) % 40))
	i=$((i + 1))
done
CHURN
setsid nohup sh /root/churn.sh >/dev/null 2>&1 &
' >/dev/null
INSTANCE="$(instance_of "$DISTRO")"
run run
expect_rc 0 "run succeeds while files are changing"
expect_has "wrote " "it reports the archive"
expect_has "test restore passed" "the test restore passed"
expect_lacks "FAIL" "no failure is reported"
FIRST="$(newest_backup)"
expect_true "an archive and its manifest exist" [ -f "$DEST/$DISTRO/$FIRST.json" ]
expect_true "no partial file is left" [ -z "$(find "$DEST/$DISTRO" -name '*.partial')" ]
expect_true "the distro kept running through the backup (same PID 1)" [ -n "$INSTANCE" -a "$(instance_of "$DISTRO")" = "$INSTANCE" ]
expect_true "no temporary distro is left registered" [ "$(distro_names)" = "$BEFORE_DISTROS" ]
expect_true "the folder for temporary distros is empty" [ -z "$(ls -A "$HOME_DIR/verify" 2>/dev/null)" ]
run list
expect_has "$FIRST" "list shows the backup"
expect_has "verified" "as verified"
run status
expect_rc 0 "status exits 0 after a verified backup"
expect_has "Last success: just now" "status shows the success"

section "5. The archive restores with plain wsl --import, without wslbak"
PLAIN="wslbak-e2e-p-$RUN_ID"
PLAIN_DIR="$SANDBOX_WIN\\plain"
extra_distros+=("$PLAIN|$PLAIN_DIR")
if wsl_exe --import "$PLAIN" "$PLAIN_DIR" "$(wslpath -w "$DEST/$DISTRO/$FIRST.tar.gz")" --version 2 </dev/null >/dev/null 2>&1; then
	ok "wsl --import accepted the archive"
	WANT="$(in_distro "$DISTRO" scripts/e2e-compare.sh)"
	GOT="$(in_distro "$PLAIN" scripts/e2e-compare.sh)"
	if [ -n "$WANT" ] && [ "$WANT" = "$GOT" ]; then
		ok "the copy matches the source: attributes, sparse file, links, names and all of /usr"
	else
		bad "the copy differs from the source" "$(diff <(echo "$WANT") <(echo "$GOT"))"
	fi
	wsl_exe --terminate "$PLAIN" </dev/null >/dev/null 2>&1
else
	bad "wsl --import rejected the archive"
fi
unregister_guarded "$PLAIN" "$PLAIN_DIR" 2>/dev/null

section "6. A damaged archive fails the test restore"
run run --no-verify
expect_rc 0 "run --no-verify succeeds"
expect_has "skipped this time" "and says the test restore was skipped"
SECOND="$(newest_backup)"
ARCHIVE="$DEST/$DISTRO/$SECOND.tar.gz"
SIZE=$(stat -c %s "$ARCHIVE")
cp "$ARCHIVE" "$SANDBOX/pristine.tar.gz"
# Flip one byte in the middle.
printf '\xff' | dd of="$ARCHIVE" bs=1 seek=$((SIZE / 2)) conv=notrunc 2>/dev/null
run verify "$SECOND"
expect_rc 2 "verify fails on an archive with one byte changed"
expect_has "the test restore failed" "and says so"
truncate -s $((SIZE / 2)) "$ARCHIVE"
run verify "$SECOND"
expect_rc 2 "verify fails on a truncated archive"
expect_true "no temporary distro is left after the failures" [ "$(distro_names)" = "$BEFORE_DISTROS" ]
expect_true "and the folder for temporary distros is empty" [ -z "$(ls -A "$HOME_DIR/verify" 2>/dev/null)" ]
cp "$SANDBOX/pristine.tar.gz" "$ARCHIVE"
run verify "$SECOND"
expect_rc 0 "verify passes again once the archive is intact"
run list
expect_true "both backups are listed as verified" [ "$(grep -c ' verified' <<<"$OUT")" = 2 ]
run verify 20000101T000000Z
expect_rc 2 "verify refuses an id that does not exist"

section "7. Only one wslbak at a time"
timeout 600 "${WB[@]}" run </dev/null >"$SANDBOX/background.log" 2>&1 &
BG=$!
# Wait until the first one has started reading.
for _ in $(seq 1 100); do
	[ -n "$(find "$DEST/$DISTRO" -name '*.partial' 2>/dev/null)" ] && break
	sleep 0.1
done
run run
expect_rc 3 "a second run exits 3 while the first is still going"
expect_has "already running" "and says why"
wait "$BG"
expect_true "the first run finished normally" [ $? = 0 ]

section "8. Retention"
# keep is 2 and three verified backups exist now, so the oldest must be gone.
expect_true "the oldest backup was deleted" [ ! -e "$DEST/$DISTRO/$FIRST.tar.gz" ]
expect_true "together with its manifest" [ ! -e "$DEST/$DISTRO/$FIRST.json" ]
expect_true "two backups remain" [ "$(backups | wc -l)" = 2 ]
# Files wslbak did not write must survive, even ones that look like backups.
echo mine >"$DEST/$DISTRO/holiday-photos.tar.gz"
echo mine >"$DEST/$DISTRO/20000101T000000Z.tar.gz"
echo mine >"$DEST/$DISTRO/notes.json"
echo mine >"$DEST/$DISTRO/important.tar.gz.partial"
run run
expect_rc 0 "another run succeeds"
for f in holiday-photos.tar.gz 20000101T000000Z.tar.gz notes.json important.tar.gz.partial; do
	expect_true "$f, which wslbak did not write, is still there" [ "$(cat "$DEST/$DISTRO/$f" 2>/dev/null)" = mine ]
done
expect_true "still exactly two real backups" [ "$(backups | grep -c -E '^20[2-9]')" = 2 ]
rm -f "$DEST/$DISTRO/holiday-photos.tar.gz" "$DEST/$DISTRO/20000101T000000Z.tar.gz" "$DEST/$DISTRO/notes.json" "$DEST/$DISTRO/important.tar.gz.partial"

section "9. A distro that only looks like a temporary one is left alone"
# Same naming pattern as wslbak's temporary distros, but stored somewhere else: wslbak must
# never remove it, neither while cleaning up leftovers nor on uninstall.
DECOY="wslbak-verify-20200101T000000Z-${RUN_ID}${RUN_ID}"
DECOY_DIR="$SANDBOX_WIN\\decoy"
extra_distros+=("$DECOY|$DECOY_DIR")
wsl_exe --import "$DECOY" "$DECOY_DIR" "$(wslpath -w "$DEST/$DISTRO/$(newest_backup).tar.gz")" --version 2 </dev/null >/dev/null 2>&1
wsl_exe --terminate "$DECOY" </dev/null >/dev/null 2>&1
if registered "$DECOY"; then
	run run
	expect_rc 0 "run succeeds with the look-alike present"
	expect_true "the look-alike is still registered after a run" registered "$DECOY"
	run_kit status
	expect_has "$DECOY  a temporary distro that belongs to wslbak" "wslbak does not offer to back it up"
else
	skip "could not create the look-alike distro"
fi

section "10. Restore"
BEFORE="$(snapshot)"
run restore --dry-run
expect_rc 0 "restore --dry-run succeeds"
expect_has "$DISTRO-restored-" "it picks a new name because the original still exists"
expect_has "Existing distros are not touched" "and says existing distros are safe"
expect_true "restore --dry-run left everything as it was" [ "$(snapshot)" = "$BEFORE" ]
run restore --name "$DISTRO" --yes
expect_rc 2 "restore refuses to reuse the name of an existing distro"
expect_has "never overwrites" "and says it never overwrites"
RESTORED="wslbak-e2e-r-$RUN_ID"
RESTORED_DIR="$SANDBOX_WIN\\restored"
extra_distros+=("$RESTORED|$RESTORED_DIR")
mkdir -p "$SANDBOX/occupied" && echo x >"$SANDBOX/occupied/file"
run restore --name "$RESTORED" --to "$SANDBOX/occupied" --yes
expect_rc 2 "restore refuses a folder that is not empty"
run restore --name 'bad name!' --yes
expect_rc 2 "restore refuses an invalid distro name"
run restore --name "$RESTORED" --to "$SANDBOX/restored" --yes
expect_rc 0 "restore succeeds under a new name"
expect_has "Restored as the distro $RESTORED" "it reports the new distro"
expect_true "the new distro is registered" registered "$RESTORED"
expect_true "the original is still registered" registered "$DISTRO"
if registered "$RESTORED"; then
	expect_true "the restored files match" [ "$(in_distro "$RESTORED" scripts/e2e-compare.sh | head -n 12)" = "$(in_distro "$DISTRO" scripts/e2e-compare.sh | head -n 12)" ]
	wsl_exe --terminate "$RESTORED" </dev/null >/dev/null 2>&1
fi
unregister_guarded "$RESTORED" "$RESTORED_DIR" 2>/dev/null

section "11. Restoring with only the backup folder (as after reinstalling Windows)"
# The copy of the program in the destination, with no settings at all.
run_kit list
expect_rc 0 "the program in the backup folder lists the backups there"
expect_has "$(newest_backup)" "including the newest one"
run_kit restore --dry-run
expect_rc 0 "and plans a restore from them"
expect_has "$DISTRO-restored-" "under a new name"
run_kit init -d "$DISTRO" --yes
expect_rc 2 "but it cannot be used to set up backups"
expect_has "from an installed wslbak" "and says where to run init from"

section "12. A distro whose tar is not GNU tar is refused"
sh_in "$DISTRO" '
cat > /usr/local/bin/tar <<"FAKE"
#!/bin/sh
echo "tar (busybox) 1.36.1"
FAKE
chmod +x /usr/local/bin/tar
' >/dev/null
run run
expect_rc 2 "run fails cleanly"
expect_has "not GNU tar" "and names the reason"
expect_true "no partial file is left behind" [ -z "$(find "$DEST/$DISTRO" -name '*.partial')" ]
run status
expect_has "not GNU tar" "status remembers the problem"
sh_in "$DISTRO" 'rm -f /usr/local/bin/tar' >/dev/null

section "13. The scheduled task really runs a backup"
COUNT_BEFORE="$(backups | wc -l)"
LAST_BEFORE="$(newest_backup)"
# A backup that succeeded less than 20 hours ago makes the scheduled run exit at once, so
# forget the earlier successes first.
rm -f "$HOME_DIR/state.json"
if win "$SYS32/schtasks.exe" /Run /TN "$(task_name)" </dev/null >/dev/null 2>&1; then
	for _ in $(seq 1 180); do
		[ "$(newest_backup)" != "$LAST_BEFORE" ] && grep -q '"lastResult": "ok"' "$HOME_DIR/state.json" 2>/dev/null && break
		sleep 1
	done
	expect_true "the task produced a new backup" [ "$(newest_backup)" != "$LAST_BEFORE" ]
	expect_true "and recorded it as a success" grep -q '"lastResult": "ok"' "$HOME_DIR/state.json"
	expect_true "the log shows it was the scheduled run" grep -q 'scheduled=true' "$HOME_DIR/wslbak.log"
	run list
	expect_has "$(newest_backup)" "the new backup is listed"
else
	bad "schtasks /Run failed"
fi
expect_true "the look-alike distro survived all of this" registered "$DECOY"

section "14. Other language"
OUT="$(timeout 60 node bin/wslbak.js --lang zh-TW --home "$HOME_DIR" status </dev/null 2>&1 | tr -d '\r')"
expect_has "排程：每天 04:30" "--lang zh-TW switches the interface to Chinese"
OUT="$(WSLBAK_LANG=zh-TW timeout 60 node bin/wslbak.js --home "$HOME_DIR" list </dev/null 2>&1 | tr -d '\r')"
expect_has "已驗證" "WSLBAK_LANG is honoured from inside WSL"

section "15. Bad arguments"
run frobnicate
expect_rc 2 "unknown command"
run run --dest "$DEST"
expect_rc 2 "an option that belongs to another command"
run init --keep 0
expect_rc 2 "--keep 0"
run init --at 25:00
expect_rc 2 "an impossible time"
run run -d some-other-distro
expect_rc 2 "a distro that is not set up"

section "16. uninstall"
BEFORE="$(snapshot)"
run uninstall --dry-run
expect_rc 0 "uninstall --dry-run succeeds"
expect_true "uninstall --dry-run left everything as it was" [ "$(snapshot)" = "$BEFORE" ]
KEPT="$(backups)"
run uninstall --yes
expect_rc 0 "uninstall succeeds"
expect_has "All backups are still there" "it says the backups are kept"
expect_true "the scheduled task is gone" task_absent
expect_true "the installed program is gone" [ ! -e "$HOME_DIR/program/wslbakw.exe" ]
expect_true "every backup is still there" [ "$(backups)" = "$KEPT" ]
expect_true "the look-alike distro survived uninstall" registered "$DECOY"
expect_true "the test distro itself was never removed" registered "$DISTRO"
unregister_guarded "$DECOY" "$DECOY_DIR" 2>/dev/null

printf '\n%d passed, %d failed, %d skipped\n' "$pass" "$fail" "$skipped"
[ "$fail" -eq 0 ]
