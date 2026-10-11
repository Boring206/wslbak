#!/usr/bin/env bash
# Checks what differential backups take for granted about GNU tar, ext4 and WSL's importer.
# wslbak's own tests cannot show these things: they are about programs it does not ship.
# Run it again when a new release of GNU tar or of WSL appears. Run inside WSL; python3 is
# needed here (not in the test distro) to read the archives and the snapshot files.
#
#   bash scripts/e2e-tarfacts.sh
#   E2E_DISTRO=OracleLinux_7_9 bash scripts/e2e-tarfacts.sh    # GNU tar 1.26
#   TARFACTS_IMPORT=1 bash scripts/e2e-tarfacts.sh             # also the wsl --import checks
#
# E2E_DISTRO and E2E_ROOTFS_URL choose the distro, as for scripts/e2e.sh; the same throwaway
# test distro is used (wslbak-e2e-<random>), and it is removed at the end unless
# KEEP_E2E_DISTRO=1 is set or it was already there. Results are kept in local/tarfacts/.
#
# What is checked, and why it matters:
#   1  The listing pass (tar --listed-incremental against /dev/null) names the same entries
#      as a real dump with the same options, except sockets and files mounted from another
#      device. A differential decides from the listing what has been deleted.
#   2  A directory that is visible at two paths makes the listing unusable, and that can be
#      told from the listing itself, from tar's warning and from /proc/self/mountinfo.
#   3  The listing pass does not open or read any file.
#   4  It writes nothing to standard error except a line per socket.
#   5  On ext4, everything that puts another inode behind a name changes that inode's
#      change time; renaming a directory does not touch what is below it.
#   6  tar --after-date chooses a file by its modification time or its change time, and
#      always includes every directory.
#   7  (TARFACTS_IMPORT=1) What wsl --import does with a directory entry that follows its
#      files, with an entry that appears twice, with a hard link to nothing and with a file
#      in the place of a directory that is not empty.
#   8  A change stamped earlier than the time asked about is missed by --after-date, and
#      the change time of its directory gives it away.
set -u

cd "$(dirname "$0")/.."
# shellcheck source=scripts/e2e-distro.sh
. scripts/e2e-distro.sh

[ -n "${WSL_DISTRO_NAME:-}" ] || { echo "Run this inside WSL."; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "python3 is needed to read the archives."; exit 2; }

# scripts/e2e-lib.sh builds the wslbak command line from this; wslbak is not run here.
HOME_DIR=/nonexistent
# shellcheck source=scripts/e2e-lib.sh
. scripts/e2e-lib.sh
RUN_ID="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
RESULTS="local/tarfacts/$FAMILY"
imported=()

cleanup() {
	local entry
	for entry in "${imported[@]:-}"; do
		[ -n "$entry" ] && unregister_guarded "${entry%%|*}" "${entry#*|}" 2>/dev/null
	done
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
# The seed script installs GNU tar where the image comes without it. strace shows which
# files the listing pass opens; python3 or perl makes a socket and writes through mmap.
# Where they cannot be installed, those checks are skipped.
in_distro "$DISTRO" scripts/e2e-seed.sh >/dev/null
sh_in "$DISTRO" '
if command -v apt-get >/dev/null 2>&1; then DEBIAN_FRONTEND=noninteractive apt-get install -y -qq strace python3-minimal e2fsprogs
elif command -v dnf >/dev/null 2>&1; then dnf install -y -q strace python3 e2fsprogs util-linux
elif command -v yum >/dev/null 2>&1; then yum install -y -q strace python3 e2fsprogs util-linux
elif command -v zypper >/dev/null 2>&1; then zypper --non-interactive --quiet install strace python3 e2fsprogs util-linux
elif command -v pacman >/dev/null 2>&1; then pacman -Sy --noconfirm --needed strace python e2fsprogs util-linux
elif command -v apk >/dev/null 2>&1; then apk add --quiet strace python3 e2fsprogs util-linux
fi' >/dev/null 2>&1
echo "  testing against $E2E_DISTRO: $(sh_in "$DISTRO" '. /etc/os-release && echo "$PRETTY_NAME"; tar --version 2>&1 | head -n 1' | tr '\n' ' ')"

section "Inside the distro"
FACTS="$(in_distro "$DISTRO" scripts/e2e-tarfacts-in.sh)"
while IFS=$'\t' read -r kind name result detail; do
	[ "$kind" = fact ] || continue
	case "$result" in
		ok) ok "$name${detail:+: $detail}" ;;
		skip) skip "$name${detail:+: $detail}" ;;
		*) bad "$name" "$detail" ;;
	esac
done <<<"$FACTS"
if [ "$(tail -n 1 <<<"$FACTS")" = finished ]; then ok "the script ran to its end"; else bad "the script ran to its end" "$(tail -n 5 <<<"$FACTS")"; fi

rm -rf "$RESULTS"
mkdir -p "$RESULTS"
# The results come out as a tar stream; the command goes in through a shell, because on
# NixOS tar is not on the PATH of a program started directly.
printf '%s\n%s\n' "$NIX_PATH_LINE" 'tar -C /wslbak-facts/out -cf - .' | win "$WSL" -d "$DISTRO" -u root -e sh -s 2>/dev/null | tar -xf - -C "$RESULTS"
printf '%s\n' "$FACTS" >"$RESULTS/facts.txt"
ANALYSIS="$(python3 scripts/e2e-tarfacts.py analyze "$RESULTS" 2>&1)"
SUMMARY="$(tail -n 1 <<<"$ANALYSIS")"
if [[ "$SUMMARY" =~ ^([0-9]+)\ passed,\ ([0-9]+)\ failed$ ]]; then
	sed '$d' <<<"$ANALYSIS"
	pass=$((pass + BASH_REMATCH[1]))
	fail=$((fail + BASH_REMATCH[2]))
else
	bad "reading the results" "$ANALYSIS"
fi
sh_in "$DISTRO" 'rm -rf /wslbak-facts' >/dev/null

if [ -n "${TARFACTS_IMPORT:-}" ]; then
	section "7. What wsl --import does with archives a merge must never produce"
	# The cases are added to Alpine's mini root file system: small, and it has a shell to
	# look at the result with.
	ARCH="$(uname -m)"
	BASE="https://dl-cdn.alpinelinux.org/alpine/latest-stable/releases/$ARCH"
	CASES="local/tarfacts/import"
	mkdir -p "$CASES"
	ROOTFS="$CASES/alpine-minirootfs.tar.gz"
	if [ ! -s "$ROOTFS" ]; then
		FILE="$(curl -fsSL "$BASE/latest-releases.yaml" | awk '/flavor: alpine-minirootfs/{f=1} f && /file:/{print $2; exit}')"
		[ -n "$FILE" ] && curl -fsSL "$BASE/$FILE" -o "$ROOTFS"
	fi
	if [ ! -s "$ROOTFS" ]; then
		skip "could not download Alpine's mini root file system"
	else
		n=0
		for case_name in $(python3 scripts/e2e-tarfacts.py import-cases "$ROOTFS" "$CASES"); do
			n=$((n + 1))
			name="wslbak-e2e-facts-$RUN_ID-$n"
			dir="$(sandbox_win)\\$name"
			imported+=("$name|$dir")
			out="$(wsl_exe --import "$name" "$dir" "$(wslpath -w "$CASES/$case_name.tar")" --version 2 2>&1 </dev/null | tr -d '\r' | sed '/^$/d'; exit "${PIPESTATUS[0]}")"
			rc=$?
			look=""
			case "$case_name" in
				dir-after-files) look='stat -c "%a %u:%g %Y" /case/late; cat /case/late/file' ;;
				dir-twice) look='stat -c "%a %u:%g %Y" /case/twice; cat /case/twice/file' ;;
				file-twice) look='cat /case/f' ;;
				link-to-nothing) look='ls -A /case' ;;
				file-over-full-dir) look='stat -c "%F" /case/x; ls -A /case/x' ;;
			esac
			seen="$(registered "$name" && sh_in "$name" "$look" | tr '\n' ' ')"
			note "$case_name: wsl --import exit $rc, says: $(head -c 300 <<<"$out" | tr '\n' ' ')"
			note "$case_name: afterwards: ${seen:-not registered}"
			# wslbak's test restore relies on the third of these (it appends its own
			# /etc/wsl.conf). A merged restore relies on none: it writes every path once,
			# directories first. The last two say that a merge that got this wrong would
			# be refused loudly instead of producing a distro that is quietly different.
			case "$case_name" in
				dir-after-files) expect_true "a directory entry that follows its files still sets mode, owner and time" [ "$seen" = "750 1234:5678 978307200 inside " ] ;;
				dir-twice) expect_true "a second entry for a directory replaces mode and owner, but the time stays that of the first" [ "$seen" = "700 1234:5678 1000000000 inside " ] ;;
				file-twice) expect_true "of two entries for a file the later one stays" [ "$seen" = "second " ] ;;
				link-to-nothing) expect_true "a hard link to an entry that is not there makes the import fail" [ "$rc" != 0 ] ;;
				file-over-full-dir) expect_true "a file in the place of a directory that is not empty makes the import fail" [ "$rc" != 0 ] ;;
			esac
			unregister_guarded "$name" "$dir" 2>/dev/null
		done
	fi
fi

finish
