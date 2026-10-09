#!/usr/bin/env bash
# Regenerates testdata/gnu.tar, the archive the unit tests use to check that wslbak's tar
# scanner understands what GNU tar really writes: pax headers, extended attributes, an ACL,
# a file capability, a sparse file over 8 GiB, hard links, a fifo, a device node, non-ASCII
# names and paths longer than 255 characters.
#
#   scripts/e2e-distro.sh create    (once, if there is no test distro yet)
#   scripts/mkfixtures.sh
#
# The archive is produced by the real backup.sh with the real options, inside the throwaway
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
# stdin, the raw tar on stdout.
{
	printf "WSLBAK_ROOT='/wslbak-root'\n"
	printf "WSLBAK_EXCLUDES='./init\n./tmp/*'\n"
	cat backup.sh
} | as_root >testdata/gnu.tar 2>testdata/gnu.stderr || true

as_root <<<'rm -rf /wslbak-root' >/dev/null
tr -d '\r' <testdata/gnu.stderr >testdata/gnu.stderr.tmp && mv testdata/gnu.stderr.tmp testdata/gnu.stderr
tar -tf testdata/gnu.tar | wc -l | tr -d ' ' >testdata/gnu.entries
echo "testdata/gnu.tar: $(stat -c %s testdata/gnu.tar) bytes, $(cat testdata/gnu.entries) entries"
cat testdata/gnu.stderr
