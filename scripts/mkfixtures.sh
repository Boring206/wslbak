#!/usr/bin/env bash
# Regenerates testdata/gnu.tar, the archive the unit tests use to check that wslbak's tar
# scanner understands what GNU tar really writes: pax headers, extended attributes, an ACL,
# a file capability, a sparse file over 8 GiB, hard links, a fifo, a device node, non-ASCII
# names and paths longer than 255 characters. With it come the files the tests of
# differential backups use: tar's listing of the same tree (gnu.snar), and, after the tree
# has been changed, another listing, a differential and a second full backup (gnu2.*).
#
#   scripts/e2e-distro.sh create    (once, if there is no test distro yet)
#   scripts/mkfixtures.sh
#
# Everything is produced by the real backup.sh with the real options, inside the throwaway
# test distro, so it only needs regenerating when backup.sh changes how it calls tar.
set -euo pipefail

cd "$(dirname "$0")/.."
# shellcheck source=scripts/e2e-distro.sh
. scripts/e2e-distro.sh
DISTRO="$(current_name)"
mkdir -p testdata

as_root() { win "$WSL" -d "$DISTRO" -u root -e sh -s; }

as_root <scripts/e2e-seed.sh >/dev/null
as_root >/dev/null <<'SH'
set -e
rm -rf /wslbak-root
mkdir -p /wslbak-root/etc /wslbak-root/tmp
cp -a /wslbak-fixture /wslbak-root/fixture
printf '[boot]\nsystemd=true\n' > /wslbak-root/etc/wsl.conf
echo 'root:x:0:0:root:/root:/bin/sh' > /wslbak-root/etc/passwd
echo 'excluded by ./tmp/*' > /wslbak-root/tmp/scratch
SH

# The same thing wslbak does: variable assignments in front of the script, the script on
# stdin, the raw tar (or tar's snapshot file) on stdout.
backup_sh() { # backup_sh <mode> [since]
	{
		printf "WSLBAK_ROOT='/wslbak-root'\n"
		printf "WSLBAK_EXCLUDES='./init\n./tmp/*'\n"
		printf "WSLBAK_MODE='%s'\n" "$1"
		printf "WSLBAK_SINCE='%s'\n" "${2:-}"
		cat backup.sh
	} | as_root
}

# A full backup, and the listing that goes with it.
backup_sh list >testdata/gnu.snar 2>/dev/null
backup_sh full >testdata/gnu.tar 2>testdata/gnu.stderr || true

# Then the tree changes in the ways a differential backup has to follow, and three more
# files are made: the listing afterwards, the differential, and a full backup of the
# changed tree. The unit tests merge gnu.tar with the differential and expect exactly
# what that second full backup holds.
SINCE="$(as_root <<'SH'
sleep 1.5
date +%s
sleep 1.5
SH
)"
SINCE="$(printf '%s' "$SINCE" | tr -d '\r\n')"
as_root >/dev/null <<'SH'
set -e
cd /wslbak-root/fixture
echo more >>plain.txt
rm acl-file
echo new >added.txt
mkdir newdir
echo inside >newdir/file
mv symlink symlink2
chmod 700 setuid-binary
ln hard1 hard3
rm fifo
echo 'a file where the fifo was' >fifo
rm devnull
mkdir devnull
echo inner >devnull/inner
rm -r "$(printf 'd%.0s' $(seq 1 120))"
SH
CLOCK="$(as_root <<<'date +%s' | tr -d '\r\n')"
backup_sh list >testdata/gnu2.snar 2>/dev/null
backup_sh diff "$SINCE" >testdata/gnu2.diff.tar 2>/dev/null || true
backup_sh full >testdata/gnu2.tar 2>/dev/null || true
printf '%s %s\n' "$SINCE" "$CLOCK" >testdata/gnu2.times

as_root <<<'rm -rf /wslbak-root' >/dev/null
tr -d '\r' <testdata/gnu.stderr | grep -v -e "$(printf '^@wslbak\tmount\t')" -e "$(printf '^@wslbak\tclock\t')" >testdata/gnu.stderr.tmp && mv testdata/gnu.stderr.tmp testdata/gnu.stderr
tar -tf testdata/gnu.tar | wc -l | tr -d ' ' >testdata/gnu.entries
echo "testdata/gnu.tar: $(stat -c %s testdata/gnu.tar) bytes, $(cat testdata/gnu.entries) entries"
echo "testdata/gnu2.diff.tar: $(stat -c %s testdata/gnu2.diff.tar) bytes, $(tar -tf testdata/gnu2.diff.tar | wc -l) entries; gnu2.tar: $(tar -tf testdata/gnu2.tar | wc -l) entries"
cat testdata/gnu.stderr
