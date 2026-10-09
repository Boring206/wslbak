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
#
# E2E_FAST=1 leaves out the parts that only exercise Windows and do not depend on the distro
# (waiting for the scheduled time, other kinds of destination, upgrading, installing the
# npm package, the console window); use it when going through several distro families.
# E2E_TOAST=1 adds a check that a Windows notification really arrives. It shows one
# notification on screen and, for the duration of the check, registers wslbak as a sender
# of notifications for the current user.
set -u

cd "$(dirname "$0")/.."
# shellcheck source=scripts/e2e-distro.sh
. scripts/e2e-distro.sh

[ -n "${WSL_DISTRO_NAME:-}" ] || { echo "Run this inside WSL."; exit 2; }
[ -f bin/wslbak-x64.exe ] || [ -f bin/wslbak-arm64.exe ] || { echo "The executables are missing; run npm run build first."; exit 2; }

RUN_ID="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
SANDBOX_WIN="$(sandbox_win)\\run-$RUN_ID"
SANDBOX="$(wslpath -u "$SANDBOX_WIN")"
HOME_DIR="$SANDBOX/home"
DEST="$SANDBOX/dest"
# Distros the tests create besides the long-lived test distro: "name|Windows folder".
extra_distros=()
# shellcheck source=scripts/e2e-lib.sh
. scripts/e2e-lib.sh
VERSION="$(node -p 'require("./package.json").version')"
fast() { [ -n "${E2E_FAST:-}" ]; }

# run_kit: like run, for the copy of the program that sits in the backup folder.
run_kit() {
	ensure_interop
	OUT="$(timeout 600 "$DEST/wslbak.exe" --lang en --home "$(wslpath -w "$SANDBOX/empty-home")" "$@" 2>&1 </dev/null | tr -d '\r'; exit "${PIPESTATUS[0]}")"
	RC=$?
}
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
	[ -n "${HOOK_PID:-}" ] && kill "$HOOK_PID" 2>/dev/null
	[ -n "${TOAST_KEY_ADDED:-}" ] && win "$REG" delete "$TOAST_KEY" /f </dev/null >/dev/null 2>&1
	# Let wslbak remove its own task and temporary distros first.
	if [ -d "$HOME_DIR" ]; then
		timeout 120 "${WB[@]}" uninstall --yes </dev/null >/dev/null 2>&1
	fi
	task_exists && win "$SYS32/schtasks.exe" /Delete /TN "$(task_name)" /F </dev/null >/dev/null 2>&1
	[ -n "${SUBST_LETTER:-}" ] && win "$SYS32/subst.exe" "$SUBST_LETTER:" /D </dev/null >/dev/null 2>&1
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
# People log in to their distro as an ordinary user, not as root; a freshly installed test
# distro has no such user yet.
DEFAULT_USER=root
wsl_exe --manage "$DISTRO" --set-default-user tester </dev/null >/dev/null 2>&1
if [ "$(as_default "$DISTRO" 'id -un')" = tester ]; then
	DEFAULT_USER=tester
	ok "the test distro logs in as an ordinary user"
else
	skip "could not make an ordinary user the default (wsl --manage --set-default-user)"
fi
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
# Give it a moment to detach: a shell that exits at once takes its background job with it.
sleep 1
' >/dev/null
INSTANCE="$(instance_of "$DISTRO")"
expect_true "files in the distro are being created and deleted" [ -n "$(sh_in "$DISTRO" 'pgrep -f churn.sh || ls /root/churn | head -n 1')" ]
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
# Any user inside the distro can give a file a name with control characters in it; a
# console would act on them.
run files /wslbak-fixture
expect_true "a control character in a file name never reaches the screen as it is" [ "$(printf '%s' "$OUT" | tr -cd '\033' | wc -c)" = 0 ]
expect_has 'esc\x1b[31mred' "it is shown in a harmless, visible form instead"
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
stat -c "%n|%F|%s|%h|%u:%g|%a|%y" plain.txt sparse.bin cap-binary setuid-binary acl-file hard1 hard2 symlink fifo devnull "中文檔名 with space.txt"
[ "$(stat -c %b sparse.bin)" -lt 100000 ] && echo sparse
[ "$(stat -c %i hard1)" = "$(stat -c %i hard2)" ] && echo linked
getcap cap-binary 2>/dev/null | sed "s|.* ||"
getfattr -d plain.txt 2>/dev/null | grep -v "^# file"
getfacl -c acl-file 2>/dev/null
readlink symlink
cat "中文檔名 with space.txt"
find . -path "./d*" -name "*.txt" | wc -c
'
sig() { printf '%s\nset -- %s\n%s\n' "$NIX_PATH_LINE" "$2" "$FIXTURE_SIG" | win "$WSL" -d "$1" -u root -e sh -s 2>&1 | no_wsl_notes; }
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
# A record in the backup folder that names another distro must not send the files there.
cp "$DEST/$DISTRO/$FIRST.json" "$SANDBOX/record.keep"
sed -i 's/"distro": "[^"]*"/"distro": "some-other-distro"/' "$DEST/$DISTRO/$FIRST.json"
run restore "$FIRST" --path /wslbak-fixture/plain.txt --into "$BACK-wrong" --yes
expect_rc 2 "restore --path refuses a backup whose record names another distro"
expect_has "belongs to some-other-distro" "and says why"
cp "$SANDBOX/record.keep" "$DEST/$DISTRO/$FIRST.json"
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
# Change one byte in the middle, to a value it certainly did not have before.
WAS="$(dd if="$ARCHIVE" bs=1 skip=$((SIZE / 2)) count=1 2>/dev/null | od -An -tu1 | tr -d ' ')"
printf "\\$(printf '%03o' $(((WAS + 1) % 256)))" | dd of="$ARCHIVE" bs=1 seek=$((SIZE / 2)) conv=notrunc 2>/dev/null
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
	expect_true "the restored files match" [ "$(in_distro "$RESTORED" scripts/e2e-compare.sh | head -n 13)" = "$(in_distro "$DISTRO" scripts/e2e-compare.sh | head -n 13)" ]
	# Identical files are not the whole story: can the restored distro be used?
	if [ "$DEFAULT_USER" = tester ]; then
		expect_true "the restored distro logs in as the same ordinary user" [ "$(as_default "$RESTORED" 'id -un')" = tester ]
		# Compared with the source rather than with fixed numbers: on Alpine, for one, a new
		# user's folders carry the setgid bit, so the mode there is 2700 and not 700.
		KEY_CHECK='stat -c "%U %a" "$HOME/.ssh" "$HOME/.ssh/id_test" | tr "\n" " "; cat "$HOME/.ssh/id_test"'
		KEY_WAS="$(as_default "$DISTRO" "$KEY_CHECK")"
		expect_true "the source has the user's private key, readable only by the user" grep -q ' 600 not a real key$' <<<"$KEY_WAS"
		expect_true "the user's private key kept its owner and mode" [ "$(as_default "$RESTORED" "$KEY_CHECK")" = "$KEY_WAS" ]
	fi
	expect_true "a setuid program is still setuid root" [ "$(sh_in "$RESTORED" 'stat -c "%u %a" /wslbak-fixture/setuid-binary')" = "0 4755" ]
	if [ "$(sh_in "$DISTRO" 'cat /proc/1/comm')" = systemd ]; then
		STARTED=no
		for _ in $(seq 1 60); do
			[ "$(sh_in "$RESTORED" 'cat /run/e2e-service-started 2>/dev/null')" = started ] && { STARTED=yes; break; }
			sleep 1
		done
		expect_true "services start in the restored distro" [ "$STARTED" = yes ]
	fi
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
expect_lacks "Other accounts on this PC" "it does not warn about a backup folder that only this account can read"

section "13d. Failures reach the webhook, and a full disk is handled"
# A listener inside WSL plays the part of the notification service.
HOOK_LOG="$SANDBOX/hook.log"
rm -f "$SANDBOX/hook.port"
node -e '
const http = require("http"), fs = require("fs");
const server = http.createServer((req, res) => {
  let body = "";
  req.on("data", (chunk) => (body += chunk));
  req.on("end", () => {
    fs.appendFileSync(process.argv[1], JSON.stringify({ url: req.url, title: req.headers.title, body }) + "\n");
    res.end("ok");
  });
});
server.listen(0, "0.0.0.0", () => fs.writeFileSync(process.argv[2], String(server.address().port)));
' "$HOOK_LOG" "$SANDBOX/hook.port" &
HOOK_PID=$!
for _ in $(seq 1 50); do [ -s "$SANDBOX/hook.port" ] && break; sleep 0.1; done
HOOK_PORT="$(cat "$SANDBOX/hook.port" 2>/dev/null)"
if [ -n "$HOOK_PORT" ] && win "$SYS32/curl.exe" -s -m 5 -o NUL "http://localhost:$HOOK_PORT/ping" </dev/null 2>/dev/null; then
	run config --webhook "http://localhost:$HOOK_PORT/hook-secret" --notify always
	expect_rc 0 "config accepts a webhook"
	expect_lacks "hook-secret" "and shows only its host, not the rest of the address"
	: >"$HOOK_LOG"
	run run
	expect_rc 0 "a run succeeds with the webhook set"
	expect_true "with notifications set to always, the success is reported" grep -q '"url":"/hook-secret"' "$HOOK_LOG"
	expect_true "the log does not contain the webhook address" bash -c "! grep -q hook-secret '$HOME_DIR/wslbak.log'"
	: >"$HOOK_LOG"
	KEPT="$(backups)"
	# The disk "fills up" two megabytes into the archive.
	run_with WSLBAK_TEST_DISK_FULL_AFTER=2000000 -- run
	expect_rc 2 "run fails when the disk fills up half-way"
	expect_has "Could not write the backup file" "and says the backup file could not be written"
	expect_true "the half-written file was removed" [ -z "$(find "$DEST/$DISTRO" -name '*.partial')" ]
	expect_true "the earlier backups are untouched" [ "$(backups)" = "$KEPT" ]
	expect_true "the failure reached the webhook" grep -q 'Could not write the backup file' "$HOOK_LOG"
	expect_true "and the message names no file" bash -c "! grep -q -e 'tar\.gz' -e 'partial' -e 'wslbak-fixture' '$HOOK_LOG'"
	run status
	expect_has "Problem in the latest run" "status shows the failed run"
	run config --webhook off --notify failure
	expect_rc 0 "the webhook is removed again"
	run run --no-verify
	expect_rc 0 "the next run succeeds"
else
	skip "Windows cannot reach a listener inside WSL on localhost"
fi
kill "$HOOK_PID" 2>/dev/null
HOOK_PID=""

section "13e. The task starts by itself when its time comes"
if fast; then
	skip "waiting for the scheduled time (E2E_FAST)"
else
	rm -f "$HOME_DIR/state.json"
	LAST_BEFORE="$(newest_backup)"
	RUNS_BEFORE="$(grep -c 'scheduled=true' "$HOME_DIR/wslbak.log" 2>/dev/null)"
	# A whole minute between 75 and 135 seconds from now, by the Windows clock.
	AT="$(win "$PS" -NoProfile -Command "(Get-Date).AddSeconds(135).ToString('HH:mm')" </dev/null 2>/dev/null | tr -d '\r')"
	POWER="$(win "$PS" -NoProfile -Command "(Get-CimInstance Win32_Battery | Select-Object -First 1).BatteryStatus" </dev/null 2>/dev/null | tr -d '\r')"
	run config --at "$AT"
	expect_rc 0 "the daily time is moved to $AT, about two minutes away"
	for _ in $(seq 1 420); do
		[ "$(newest_backup)" != "$LAST_BEFORE" ] && grep -q '"lastResult": "ok"' "$HOME_DIR/state.json" 2>/dev/null && break
		sleep 1
	done
	expect_true "a backup appeared without anybody starting the task" [ "$(newest_backup)" != "$LAST_BEFORE" ]
	expect_true "it was the scheduled run" [ "$(grep -c 'scheduled=true' "$HOME_DIR/wslbak.log" 2>/dev/null)" -gt "${RUNS_BEFORE:-0}" ]
	expect_true "and it succeeded" grep -q '"lastResult": "ok"' "$HOME_DIR/state.json"
	case "$POWER" in
		1) echo "  (the PC was running on battery)" ;;
		"") ;;
		*) echo "  (the PC was on mains power)" ;;
	esac
	run config --at 04:30
fi

section "13f. A Windows notification really arrives"
TOAST_KEY='HKCU\Software\Classes\AppUserModelId\Boring206.wslbak'
if [ -z "${E2E_TOAST:-}" ]; then
	skip "showing a notification on screen (set E2E_TOAST=1 to include it)"
elif ! command -v python3 >/dev/null 2>&1; then
	skip "python3 is needed to read the notification store"
else
	# Windows keeps notifications in a small database. This prints how many of them are
	# wslbak's, followed by the text of the newest one.
	toasts() {
		local copy="$SANDBOX/wpn" store
		store="$(wslpath -u "$(win "$SYS32/cmd.exe" /c 'echo %LOCALAPPDATA%' </dev/null 2>/dev/null | tr -d '\r')")/Microsoft/Windows/Notifications"
		mkdir -p "$copy" && cp "$store"/wpndatabase.db* "$copy"/ 2>/dev/null
		python3 - "$copy/wpndatabase.db" <<'PY'
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
rows = db.execute("""select n.Payload from Notification n join NotificationHandler h on n.HandlerId = h.RecordId
                     where h.PrimaryId = 'Boring206.wslbak' and n.Type = 'toast' order by n.Id""").fetchall()
last = rows[-1][0] if rows else b""
print(len(rows), (last.decode("utf-8", "replace") if isinstance(last, bytes) else str(last)).replace("\n", " "))
PY
	}
	# Windows only shows notifications from a sender it knows; init registers wslbak as one,
	# except in the sandbox. Do the same for the length of this check.
	if ! win "$REG" query "$TOAST_KEY" </dev/null >/dev/null 2>&1; then
		win "$REG" add "$TOAST_KEY" /v DisplayName /d wslbak /f </dev/null >/dev/null 2>&1 && TOAST_KEY_ADDED=1
	fi
	TOASTS_BEFORE="$(toasts | cut -d' ' -f1)"
	sed -i 's/"toast": false/"toast": true/' "$HOME_DIR/config.json"
	run_with WSLBAK_TEST_TOAST=1 WSLBAK_TEST_DISK_FULL_AFTER=2000000 -- run
	expect_rc 2 "a run that fails raises a notification"
	NEWEST=""
	for _ in $(seq 1 20); do
		NEWEST="$(toasts)"
		[ "${NEWEST%% *}" -gt "${TOASTS_BEFORE:-0}" ] 2>/dev/null && break
		sleep 1
	done
	expect_true "Windows accepted it and put it in the notification centre" [ "${NEWEST%% *}" -gt "${TOASTS_BEFORE:-0}" ]
	expect_true "it carries the short description of the problem" grep -q 'Could not write the backup file' <<<"$NEWEST"
	expect_true "and no file name" bash -c '! grep -q -e "tar\.gz" -e "partial" <<<"$0"' "$NEWEST"
	sed -i 's/"toast": true/"toast": false/' "$HOME_DIR/config.json"
	if [ -n "${TOAST_KEY_ADDED:-}" ]; then
		win "$REG" delete "$TOAST_KEY" /f </dev/null >/dev/null 2>&1
		TOAST_KEY_ADDED=""
	fi
	run run --no-verify
	expect_rc 0 "the next run succeeds"
fi

section "14. Other language"
OUT="$(timeout 60 node bin/wslbak.js --lang zh-TW --home "$HOME_DIR" status </dev/null 2>&1 | tr -d '\r')"
expect_has "排程：每天 04:30" "--lang zh-TW switches the interface to Chinese"
OUT="$(WSLBAK_LANG=zh-TW timeout 60 node bin/wslbak.js --home "$HOME_DIR" list </dev/null 2>&1 | tr -d '\r')"
expect_has "已驗證" "WSLBAK_LANG is honoured from inside WSL"

section "14b. In a real console window"
# Everything above reads the program's output through a pipe. A console is different: the
# program writes to it in another way, the console has a code page and its own idea of how
# wide a character is, and questions are answered from the keyboard.
EXE="bin/wslbak-$([ "$(uname -m)" = aarch64 ] && echo arm64 || echo x64).exe"
if fast; then
	skip "the console window (E2E_FAST)"
elif [ ! -f bin/e2e-console.exe ]; then
	skip "bin/e2e-console.exe is missing (npm run build makes it)"
else
	# console <code page> <keys> <text to wait for> <arguments>: run wslbak in a hidden
	# console of its own. The screen ends up in OUT, its cell map in CELLS, the exit code in RC.
	console() {
		local cp="$1" keys="$2" after="$3" result="$SANDBOX/console.txt"
		shift 3
		rm -f "$result"
		win "$PWD/bin/e2e-console.exe" -out "$(wslpath -w "$result")" -cp "$cp" -type "$keys" -after "$after" -timeout 240 -- \
			"$(wslpath -w "$PWD/$EXE")" --home "$(wslpath -w "$HOME_DIR")" "$@" </dev/null >/dev/null 2>&1
		RC="$(sed -n '1s/^exit=//p' "$result" 2>/dev/null)"
		OUT="$(sed -n '/^\[text\]$/,/^\[cells\]$/p' "$result" 2>/dev/null | sed '1d;$d')"
		CELLS="$(sed -n '/^\[cells\]$/,$p' "$result" 2>/dev/null | sed '1d')"
	}
	# Where the columns of the backup table start, counted in console cells, for the header
	# and every row: one line per distinct layout, so exactly one line means aligned.
	table_layouts() {
		local first last
		first="$(grep -n -E '^  [0-9]{8}T[0-9]{6}Z' <<<"$OUT" | head -n 1 | cut -d: -f1)"
		last="$(grep -n -E '^  [0-9]{8}T[0-9]{6}Z' <<<"$OUT" | tail -n 1 | cut -d: -f1)"
		[ -n "$first" ] || return 0
		sed -n "$((first - 1)),${last}p" <<<"$CELLS" | awk '{
			out = ""; gap = 2
			for (i = 1; i <= length($0); i++) {
				if (substr($0, i, 1) == " ") gap++
				else { if (gap >= 2) out = out " " i; gap = 0 }
			}
			print out
		}' | sort -u
	}
	console 950 "" "" --lang zh-TW list
	expect_rc 0 "list runs in a console set to code page 950 (Traditional Chinese)"
	expect_has "已驗證" "Chinese text is shown as Chinese"
	expect_lacks "[0m" "colour codes are interpreted, not printed"
	expect_true "the table columns line up, counted the way the console counts width" [ "$(table_layouts | wc -l)" = 1 ]
	console 437 "" "" --lang zh-TW list
	expect_has "已驗證" "the text is still right in a console set to code page 437"
	expect_true "and the columns still line up" [ "$(table_layouts | wc -l)" = 1 ]
	console 950 'n\r' '[y/N]' --lang en uninstall
	expect_has "Proceed? [y/N] n" "a question can be answered from the keyboard"
	expect_true "answering n leaves the scheduled task in place" task_exists
	expect_true "and the installed program" [ -f "$HOME_DIR/program/wslbakw.exe" ]
	# init asks twice: whether to go ahead, and whether to make the first backup right away.
	console 950 '是\rn\r' '[y/N]' --lang zh-TW init -d "$DISTRO" --dest "$(wslpath -w "$DEST")" --keep 2 --at 4:30
	expect_rc 0 "typing 是 at the Chinese question goes ahead"
	expect_has "設定完成" "and the setup is carried out"
fi

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

section "17. Backups on a network share"
# The administrative share of the drive the sandbox is on, reached through the loopback
# address: a real SMB path to the same folder. Not every account may use it.
SMB_DEST="\\\\localhost\\${SANDBOX_WIN%%:*}\$${SANDBOX_WIN#?:}\\smb-dest"
if fast; then
	skip "the network share (E2E_FAST)"
elif [ "$(SMB_DEST="$SMB_DEST" WSLENV="${WSLENV:+$WSLENV:}SMB_DEST" win "$PS" -NoProfile -Command 'New-Item -ItemType Directory -Force -Path $env:SMB_DEST -ErrorAction SilentlyContinue | Out-Null; if (Test-Path -LiteralPath $env:SMB_DEST) { "ok" }' </dev/null 2>/dev/null | tr -d '\r')" != ok ]; then
	skip "this account cannot write to $SMB_DEST"
else
	run init -d "$DISTRO" --dest "$SMB_DEST" --keep 2 --yes
	expect_rc 0 "init accepts a destination on a network share"
	run run
	expect_rc 0 "a backup to the share succeeds"
	expect_has "test restore passed" "and passes its test restore"
	expect_true "the archive is on the share" [ -n "$(find "$SANDBOX/smb-dest/$DISTRO" -name '*.tar.gz' 2>/dev/null)" ]
	expect_true "with no partial file left" [ -z "$(find "$SANDBOX/smb-dest" -name '*.partial' 2>/dev/null)" ]
	run list
	expect_has "verified" "list reads the backups from the share"
	run files --find plain.txt
	expect_rc 0 "files reads the index from the share"
	run restore --dry-run
	expect_rc 0 "restore plans a restore from the share"
	run doctor
	expect_true "doctor copes with a destination that has no drive letter" [ "$RC" = 0 -o "$RC" = 1 ]
	expect_has "smb-dest" "and reports on it"
	run uninstall --yes
	expect_rc 0 "uninstall succeeds"
fi

section "18. A backup drive that is not there"
# A drive letter that can be made to disappear stands in for an external drive that is
# unplugged: subst maps a letter to a folder, for this Windows session only.
SUBST_LETTER=""
if fast; then
	skip "the disappearing drive (E2E_FAST)"
else
	for letter in W V U T S R Q P; do
		if [ "$(win "$SYS32/cmd.exe" /c "if exist $letter:\\ (echo used) else (echo free)" </dev/null 2>/dev/null | tr -d '\r')" = free ]; then
			SUBST_LETTER="$letter"
			break
		fi
	done
	mkdir -p "$SANDBOX/drive"
	if [ -n "$SUBST_LETTER" ] && win "$SYS32/subst.exe" "$SUBST_LETTER:" "$SANDBOX_WIN\\drive" </dev/null >/dev/null 2>&1; then
		run init -d "$DISTRO" --dest "$SUBST_LETTER:\\backup" --keep 2 --yes
		expect_rc 0 "init accepts a destination on the drive $SUBST_LETTER:"
		run run --no-verify
		expect_rc 0 "a backup to the drive succeeds"
		ON_DRIVE="$(find "$SANDBOX/drive/backup/$DISTRO" -name '*.tar.gz' 2>/dev/null | wc -l)"
		expect_true "the archive is on the drive" [ "$ON_DRIVE" = 1 ]
		win "$SYS32/subst.exe" "$SUBST_LETTER:" /D </dev/null >/dev/null 2>&1
		run run --no-verify
		expect_rc 2 "with the drive gone, run fails"
		expect_has "$SUBST_LETTER:" "and names the place it could not reach"
		expect_lacks "goroutine" "without crashing"
		run status
		expect_has "Problem in the latest run" "status shows the failed run"
		run doctor
		expect_true "doctor reports a problem" [ "$RC" = 1 -o "$RC" = 2 ]
		expect_has "$SUBST_LETTER:" "and names the drive"
		win "$SYS32/subst.exe" "$SUBST_LETTER:" "$SANDBOX_WIN\\drive" </dev/null >/dev/null 2>&1
		run run --no-verify
		expect_rc 0 "once the drive is back, the next run succeeds"
		expect_true "and adds a backup next to the first" [ "$(find "$SANDBOX/drive/backup/$DISTRO" -name '*.tar.gz' | wc -l)" = 2 ]
		run uninstall --yes
		expect_rc 0 "uninstall succeeds"
		win "$SYS32/subst.exe" "$SUBST_LETTER:" /D </dev/null >/dev/null 2>&1
	else
		skip "no free drive letter for subst"
	fi
	SUBST_LETTER=""
fi

section "19. Two distros under one schedule"
MULTI="wslbak-e2e-m-$RUN_ID"
MULTI_DIR="$SANDBOX_WIN\\multi"
if fast; then
	skip "the second distro (E2E_FAST)"
else
	extra_distros+=("$MULTI|$MULTI_DIR")
	wsl_exe --import "$MULTI" "$MULTI_DIR" "$(wslpath -w "$DEST/$DISTRO/$(newest_backup).tar.gz")" --version 2 </dev/null >/dev/null 2>&1
	if registered "$MULTI"; then
		# Left to itself, init never picks a test distro. This switch turns that around, so
		# that --all and the question "which distro?" can be tried without touching any other
		# distro on this PC. (Other test distros that happen to exist are included.)
		ONLY=WSLBAK_TEST_ONLY_TEST_DISTROS=1
		# The question is only asked when init is going to do something, so answer it, look at
		# the plan that follows, and then decline to go ahead.
		BEFORE="$(snapshot)"
		ensure_interop
		OUT="$(printf '1\nn\n' | env "$ONLY" WSLENV="${WSLENV:+$WSLENV:}${ONLY%%=*}" timeout 120 "${WB[@]}" init --dest "$DEST" 2>&1 | tr -d '\r'; exit "${PIPESTATUS[1]}")"
		RC=$?
		expect_rc 0 "without -d, init asks which distro and takes a number for an answer"
		expect_has "[2] " "the question numbers the distros to choose from"
		expect_has "$MULTI" "and names them"
		expect_has "About to set up" "the plan for the chosen one follows"
		expect_true "declining at the next question leaves everything as it was" [ "$(snapshot)" = "$BEFORE" ]
		run_with "$ONLY" -- init --all --dest "$DEST" --keep 2 --yes
		expect_rc 0 "init --all sets up every distro that can be backed up"
		expect_has "$DISTRO" "the test distro"
		expect_has "$MULTI" "and the second one"
		run config -d "$MULTI"
		expect_rc 0 "the second distro has settings of its own"
		run run
		expect_rc 0 "one run backs up both, each with its test restore"
		MULTI_COUNT="$(find "$DEST/$MULTI" -name '*.tar.gz' 2>/dev/null | wc -l)"
		expect_true "the second distro has its own folder of backups" [ "$MULTI_COUNT" = 1 ]
		run list
		expect_has "$MULTI" "list shows the second distro"
		expect_has "$DISTRO  " "next to the first"
		run status
		expect_rc 0 "status is content with both"
		run config -d "$MULTI" --disable
		expect_rc 0 "one of them can be turned off"
		run run --no-verify
		expect_rc 0 "the next run succeeds"
		expect_true "and leaves the disabled distro alone" [ "$(find "$DEST/$MULTI" -name '*.tar.gz' | wc -l)" = "$MULTI_COUNT" ]
		run uninstall --yes
		expect_rc 0 "uninstall succeeds"
		wsl_exe --terminate "$MULTI" </dev/null >/dev/null 2>&1
	else
		skip "could not create the second distro"
	fi
	unregister_guarded "$MULTI" "$MULTI_DIR" 2>/dev/null
fi

section "20. Upgrading from an older version"
OLD="$SANDBOX/old"
if fast; then
	skip "the upgrade (E2E_FAST)"
elif node scripts/build.mjs --as 0.0.1 --into "$OLD" >/dev/null 2>&1 && [ -f "$OLD/wslbak.exe" ]; then
	old() {
		ensure_interop
		OUT="$(timeout 600 "$OLD/wslbak.exe" --lang en --home "$(wslpath -w "$HOME_DIR")" "$@" 2>&1 </dev/null | tr -d '\r'; exit "${PIPESTATUS[0]}")"
		RC=$?
	}
	installed_version() {
		ensure_interop
		timeout 60 "$HOME_DIR/program/wslbak.exe" --version </dev/null 2>/dev/null | tr -d '\r'
	}
	old init -d "$DISTRO" --dest "$(wslpath -w "$DEST")" --keep 2 --yes
	expect_rc 0 "version 0.0.1 sets up backups"
	expect_true "and installs itself" [ "$(installed_version)" = "wslbak 0.0.1" ]
	run status
	expect_true "running the newer version replaces the installed copy" [ "$(installed_version)" = "wslbak $VERSION" ]
	expect_true "the scheduled task is still there" task_exists
	old status
	expect_true "running the older version again does not put the old copy back" [ "$(installed_version)" = "wslbak $VERSION" ]
	run run --no-verify
	expect_rc 0 "the newer version backs up with the settings the older one wrote"
	old list
	expect_rc 0 "the older version can still read the backups"
	expect_has "$(newest_backup)" "including the one the newer version made"
	run uninstall --yes
	expect_rc 0 "uninstall succeeds"
else
	skip "Go is not available here to build an older version"
fi

section "21. Installing the packed npm package"
if fast; then
	skip "the npm package (E2E_FAST)"
elif ! command -v npm >/dev/null 2>&1; then
	skip "npm is not installed"
else
	TGZ="$(npm pack --ignore-scripts --silent --pack-destination "$SANDBOX" 2>/dev/null | tail -n 1)"
	if [ -n "$TGZ" ] && [ -f "$SANDBOX/$TGZ" ]; then
		ok "npm pack made $TGZ"
		if npm install --global --prefix "$SANDBOX/npm-wsl" --silent --no-audit --no-fund "$SANDBOX/$TGZ" >/dev/null 2>&1; then
			ensure_interop
			OUT="$(timeout 120 "$SANDBOX/npm-wsl/bin/wslbak" --version </dev/null 2>&1 | tr -d '\r')"
			expect_true "installed inside WSL, the command runs" [ "$OUT" = "wslbak $VERSION" ]
			OUT="$(timeout 120 "$SANDBOX/npm-wsl/bin/wslbak" --lang en --home "$HOME_DIR" init -d "$DISTRO" --dest "$DEST" --dry-run </dev/null 2>&1 | tr -d '\r')"
			expect_has "About to set up" "and can plan a setup"
		else
			bad "npm install of the packed package failed inside WSL"
		fi
		if win "$SYS32/where.exe" npm.cmd </dev/null >/dev/null 2>&1; then
			OUT="$(NPM_PREFIX="$SANDBOX_WIN\\npm-win" NPM_TGZ="$SANDBOX_WIN\\$TGZ" WSLENV="${WSLENV:+$WSLENV:}NPM_PREFIX:NPM_TGZ" win "$PS" -NoProfile -Command '& npm.cmd install --global --prefix $env:NPM_PREFIX --silent --no-audit --no-fund $env:NPM_TGZ | Out-Null; & (Join-Path $env:NPM_PREFIX "wslbak.cmd") --version' </dev/null 2>&1 | tr -d '\r')"
			expect_true "installed on Windows, the command runs" [ "$OUT" = "wslbak $VERSION" ]
		else
			skip "npm is not installed on Windows"
		fi
	else
		bad "npm pack did not produce a package"
	fi
fi

finish
