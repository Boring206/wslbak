#!/bin/sh
# Prints a description of the test files and of /usr. Run in the source distro and in a
# restored copy: the two outputs must be identical.
#
# acl-file is left out on purpose: WSL's importer does not restore POSIX ACLs (the ACL is
# in the archive, but it is gone after wsl --import), and the file mode changes with it.
LC_ALL=C
export LC_ALL
cd /wslbak-fixture || exit 1
stat -c '%n|%F|%s|%h|%u:%g|%a|%y' plain.txt sparse.bin cap-binary hard1 hard2 symlink fifo devnull '中文檔名 with space.txt'
echo "sparse stays sparse: $([ "$(stat -c %b sparse.bin)" -lt 100000 ] && echo yes || echo no)"
echo "hard links share an inode: $([ "$(stat -c %i hard1)" = "$(stat -c %i hard2)" ] && echo yes || echo no)"
getcap cap-binary 2>/dev/null
getfattr -d plain.txt 2>/dev/null
readlink symlink
cat '中文檔名 with space.txt'
find . -path './d*' -name '*.txt' | wc -c
dd if=sparse.bin bs=1 skip=1000000 count=4 2>/dev/null
echo
# Every file under /usr: path, type, size, mode, owner, link target; then the contents of
# the small ones.
find /usr -xdev -type f -printf '%p|%s|%m|%U:%G\n' | sort | sha256sum
find /usr -xdev ! -type f -printf '%p|%y|%m|%U:%G|%l\n' | sort | sha256sum
find /usr/bin /etc/passwd /etc/group -xdev -type f -size -512k -print0 | sort -z | xargs -0 sha256sum | sha256sum
