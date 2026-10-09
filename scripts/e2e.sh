#!/usr/bin/env bash
# End-to-end tests for wslbak. Run inside WSL:
#   npm run build && npm run e2e
#
# Nothing here touches a real distro or the real wslbak settings. Everything runs against
# a throwaway Debian distro (wslbak-e2e-<random>, see scripts/e2e-distro.sh) and a sandbox
# folder passed with --home, under %LOCALAPPDATA%\wslbak-e2e. The test distro is created
# on first use and kept for the next run; remove it with: scripts/e2e-distro.sh destroy
#
# To test against another distro family, set E2E_DISTRO to a name from
# `wsl --list --online` (FedoraLinux-44, archlinux, openSUSE-Tumbleweed, …) or to alpine:
#   E2E_DISTRO=FedoraLinux-44 npm run e2e
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
	ensure_interop
	OUT="$(timeout 600 "${WB[@]}" "$@" 2>&1 </dev/null | tr -d '\r'; exit "${PIPESTATUS[0]}")"
	RC=$?
}
# run_kit: the same for the copy of the program that sits in the backup folder.
run_kit() {
	ensure_interop
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

# wsl.exe prints its own warnings (lines starting with "wsl: ") next to the command's
# output; they are not part of what the command said.
no_wsl_notes() { tr -d '\r' | grep -v '^wsl: ' || true; }
# in_distro <distro> <script file>: run a script as root inside a distro.
in_distro() { win "$WSL" -d "$1" -u root -e sh -s <"$2" 2>&1 | no_wsl_notes; }
# sh_in <distro> <commands>: run shell commands as root inside a distro. They travel on
# stdin, so nothing has to survive wsl.exe's handling of quotes.
sh_in() { printf '%s\n' "$2" | win "$WSL" -d "$1" -u root -e sh -s 2>&1 | no_wsl_notes; }
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
echo "  testing against $E2E_DISTRO: $(sh_in "$DISTRO" '. /etc/os-release && echo "$PRETTY_NAME"; tar --version 2>&1 | head -n 1' | tr '\n' ' ')"
# A distro that only has BusyBox tar (Alpine as it comes), or no tar at all (openSUSE
# Tumbleweed as it comes), must be refused with the reason and the command that fixes it.
if ! sh_in "$DISTRO" 'tar --version 2>/dev/null' | grep -q 'GNU tar'; then
	run init -d "$DISTRO" --dest "$DEST" --dry-run
	expect_rc 2 "a distro without GNU tar is refused"
	if sh_in "$DISTRO" 'command -v tar' | grep -q tar; then
		expect_has "not GNU tar" "because its tar is not GNU tar"
	else
		expect_has "has no tar installed" "because it has no tar"
	fi
	expect_has "apk add tar" "and the command that fixes it is given"
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

section "4b. Looking inside a backup"
expect_true "the backup has a file index next to it" [ -f "$DEST/$DISTRO/$FIRST.idx.gz" ]
run files
expect_rc 0 "files lists the root of the newest backup"
expect_has "etc/" "the listing shows /etc"
expect_has "wslbak-fixture/" "and the folder with the test files"
run files "$FIRST" /wslbak-fixture
expect_rc 0 "files lists a folder of a named backup"
expect_has "plain.txt" "a plain file is listed"
expect_has "symlink -> plain.txt" "a symbolic link shows its target"
# Which of the two names tar met first (and stored as the file) depends on the order of
# the directory on disk, so find out which one is recorded as the link.
if grep -qF 'hard2 = /wslbak-fixture/hard1' <<<"$OUT"; then
	LINKED=hard2 LINK_TARGET=hard1
else
	LINKED=hard1 LINK_TARGET=hard2
fi
expect_has "$LINKED = /wslbak-fixture/$LINK_TARGET" "a hard link shows what it is linked to"
expect_has "sparse.bin" "the sparse file is listed"
expect_has "9.0 GB" "with its full size"
run files /wslbak-fixture/plain.txt
expect_has "1234:5678" "a single file shows its owner"
run files --find "with space"
expect_rc 0 "files --find finds a name with a space and Chinese characters"
expect_has "/wslbak-fixture/中文檔名 with space.txt" "and prints its full path"
run files /no/such/folder
expect_rc 1 "files exits 1 for a path that is not in the backup"
run files --find no-such-name-anywhere
expect_rc 1 "files --find exits 1 when nothing matches"
run files /etc/../root
expect_rc 2 "files refuses a path with .."

section "4c. Bringing back single files"
BACK="/root/wslbak-back-$RUN_ID"
INSIDE_BEFORE="$(sh_in "$DISTRO" "ls -d /root/wslbak-back-* 2>/dev/null | wc -l")"
run restore --path /wslbak-fixture --into "$BACK" --dry-run
expect_rc 0 "restore --path --dry-run succeeds"
expect_has "no existing file is overwritten" "the plan says nothing is overwritten"
expect_true "the dry run created nothing inside the distro" [ "$(sh_in "$DISTRO" "ls -d /root/wslbak-back-* 2>/dev/null | wc -l")" = "$INSIDE_BEFORE" ]
run restore --path /wslbak-fixture --into "$BACK" --yes
expect_rc 0 "restore --path brings a folder back into the distro"
expect_has "Brought back" "and reports it"
# The files went through GNU tar inside the distro, so everything is preserved, ACLs included
# (unlike a whole-distro restore through wsl --import).
FIXTURE_SIG='
LC_ALL=C; export LC_ALL
cd "$1" || exit 1
stat -c "%n|%F|%s|%h|%u:%g|%a|%y" plain.txt sparse.bin cap-binary acl-file hard1 hard2 symlink fifo devnull "中文檔名 with space.txt"
[ "$(stat -c %b sparse.bin)" -lt 100000 ] && echo sparse
[ "$(stat -c %i hard1)" = "$(stat -c %i hard2)" ] && echo linked
getcap cap-binary 2>/dev/null | sed "s|.* ||"
getfattr -d plain.txt 2>/dev/null | grep -v "^# file"
getfacl -c acl-file 2>/dev/null
readlink symlink
cat "中文檔名 with space.txt"
find . -path "./d*" -name "*.txt" | wc -c
'
sig() { printf 'set -- %s\n%s\n' "$2" "$FIXTURE_SIG" | win "$WSL" -d "$1" -u root -e sh -s 2>&1 | no_wsl_notes; }
WANT="$(sig "$DISTRO" /wslbak-fixture)"
GOT="$(sig "$DISTRO" "$BACK/wslbak-fixture")"
if [ -n "$WANT" ] && [ "$WANT" = "$GOT" ]; then
	ok "the files that came back match the originals, ACLs included"
else
	bad "the files that came back differ from the originals" "$(diff <(echo "$WANT") <(echo "$GOT"))"
fi
expect_true "the new folder belongs to the owner of the folder it is in" [ "$(sh_in "$DISTRO" "stat -c %u:%g $BACK")" = "$(sh_in "$DISTRO" "stat -c %u:%g /root")" ]
expect_true "the temporary link was removed" [ -z "$(sh_in "$DISTRO" 'ls /run/wslbak-* /dev/shm/wslbak-* /tmp/wslbak-* 2>/dev/null')" ]
run restore --path /wslbak-fixture/plain.txt --into "$BACK" --yes
expect_rc 2 "restore --path refuses a folder that is not empty"
expect_has "not empty" "and says why"
# A hard link whose other name is outside the selection: both names must come back.
run restore --path "/wslbak-fixture/$LINKED" --into "$BACK-link" --yes
expect_rc 0 "restore --path brings back a single hard-linked file"
expect_true "together with the file it is linked to" [ "$(sh_in "$DISTRO" "cd $BACK-link/wslbak-fixture && [ \"\$(stat -c %i hard1)\" = \"\$(stat -c %i hard2)\" ] && cat $LINKED")" = link ]
run restore --path /no/such/file --into "$BACK-none" --yes
expect_rc 2 "restore --path refuses a path that is not in the backup"
expect_true "and creates nothing for it" [ -z "$(sh_in "$DISTRO" "ls -d $BACK-none 2>/dev/null")" ]
run restore --path / --into "$BACK-root" --yes
expect_rc 2 "restore --path refuses the whole root"
run restore --path /etc/hostname --into relative/folder --yes
expect_rc 2 "restore --path refuses a target that is not an absolute path"
run restore --path /etc/hostname --yes
expect_rc 2 "--path needs --into"
sh_in "$DISTRO" "rm -rf /root/wslbak-back-$RUN_ID /root/wslbak-back-$RUN_ID-link" >/dev/null

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
ensure_interop
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
expect_true "and its file index" [ ! -e "$DEST/$DISTRO/$FIRST.idx.gz" ]
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
# Task Scheduler sometimes answers "the task is disabled" for a task that is enabled and
# ready (seen within a couple of minutes of registering it, with third-party antivirus
# installed). Asking again shortly afterwards works, so try for up to a minute.
TASK_STARTED=1
for _ in $(seq 1 12); do
	TASK_OUT="$(win "$SYS32/schtasks.exe" /Run /TN "$(task_name)" </dev/null 2>&1)" && { TASK_STARTED=0; break; }
	sleep 5
done
if [ "$TASK_STARTED" = 0 ]; then
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
	bad "schtasks /Run failed" "$(printf '%s' "$TASK_OUT" | iconv -f CP950 -t UTF-8 2>/dev/null || printf '%s' "$TASK_OUT")"
fi
expect_true "the look-alike distro survived all of this" registered "$DECOY"

section "13b. Changing settings"
run config
expect_rc 0 "config shows the settings"
expect_has "every day at 04:30" "including the schedule"
expect_has "the newest 2 backups" "and how many backups are kept"
BEFORE="$(snapshot)"
run config --keep 9 --dry-run
expect_rc 0 "config --dry-run succeeds"
expect_has "--keep: 2 → 9" "and shows what would change"
expect_true "config --dry-run left everything as it was" [ "$(snapshot)" = "$BEFORE" ]
run config --keep 5 --keep-weekly 2 --exclude '/root/churn/*' --notify always
expect_rc 0 "config changes several settings at once"
expect_has "Saved" "and saves them"
run config
expect_has "the newest 5 backups" "the new number of backups is shown"
expect_has "one per week for 2 weeks" "with the weekly rule"
expect_has "./root/churn/*" "and the new exclude"
expect_has "after every backup" "and the new notification rule"
run config --keep 5
expect_has "Nothing changed" "setting a value it already has changes nothing"
run config --at 05:15
expect_rc 0 "config --at succeeds"
expect_true "and the scheduled task now starts at the new time" bash -c "cd '$SYS32' && ./schtasks.exe /Query /TN '$(task_name)' /XML </dev/null 2>/dev/null | tr -d '\\r\\0' | grep -q 'T05:15:00'"
run config --unexclude /init
expect_rc 2 "config refuses to stop excluding /init"
run config -d some-other-distro --keep 3
expect_rc 2 "config refuses a distro that is not set up"
run config --keep 2 --keep-weekly 0 --unexclude '/root/churn/*' --notify failure --at 04:30
expect_rc 0 "config puts the settings back"
run run
expect_rc 0 "a backup still succeeds after the settings were changed"

section "13c. doctor"
run doctor
expect_true "doctor does not report a problem (exit 0 or 1)" [ "$RC" = 0 -o "$RC" = 1 ]
expect_has "$DISTRO: can be backed up" "it checks the test distro"
expect_has "Smart App Control" "it checks Smart App Control"
expect_has "Schedule: every day at 04:30" "it finds the scheduled task"
expect_has "the last success was" "and the last successful backup"

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
