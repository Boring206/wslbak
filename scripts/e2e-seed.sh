#!/bin/sh
# Runs as root inside the throwaway test distro. It plants the kinds of files that a
# backup tool tends to get wrong, under /wslbak-fixture: a sparse file larger than 8 GiB,
# an extended attribute, a file capability, an ACL, hard links, a fifo, a device node,
# non-ASCII names and very long paths.
set -e
LC_ALL=C
export LC_ALL DEBIAN_FRONTEND=noninteractive

# setcap, setfacl and setfattr are not in a minimal image. Without network access they
# stay missing, and the files that need them are simply not created.
if ! command -v setcap >/dev/null 2>&1 || ! command -v setfacl >/dev/null 2>&1 || ! command -v setfattr >/dev/null 2>&1; then
	apt-get update -qq >/dev/null 2>&1 || true
	apt-get install -y -qq libcap2-bin acl attr >/dev/null 2>&1 || true
fi

rm -rf /wslbak-fixture
mkdir -p /wslbak-fixture
cd /wslbak-fixture

echo hello >plain.txt
touch -d '2020-02-03 04:05:06.123456789 UTC' plain.txt
chown 1234:5678 plain.txt
if command -v setfattr >/dev/null 2>&1; then setfattr -n user.wslbak -v hello plain.txt; fi

truncate -s 9G sparse.bin
printf 'data' | dd of=sparse.bin bs=1 seek=1000000 conv=notrunc 2>/dev/null

cp /bin/true cap-binary
if command -v setcap >/dev/null 2>&1; then setcap cap_net_raw+ep cap-binary; fi

echo acl >acl-file
if command -v setfacl >/dev/null 2>&1; then setfacl -m u:1234:rw acl-file; fi

echo link >hard1
ln hard1 hard2
ln -s plain.txt symlink
mkfifo fifo
mknod devnull c 1 3
echo 中文內容 >'中文檔名 with space.txt'

long=$(printf 'd%.0s' $(seq 1 120))
mkdir -p "$long/$long"
echo deep >"$long/$long/$(printf 'f%.0s' $(seq 1 150)).txt"
echo seeded
