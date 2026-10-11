#!/bin/sh
# Prints a description of everything under /wslbak-diff, and of /etc/wsl.conf. Run in the
# source distro and in a copy restored from a differential backup: the two outputs must be
# identical. Every entry with its type, size, mode, owner, modification time and link
# target; which names are one file (hard links); capabilities and attributes; and the
# contents of every file.
LC_ALL=C
export LC_ALL
cd /wslbak-diff || exit 1
# The size of a directory is left out: it says how much room its list of names takes, which
# depends on how the directory came to be, not on what is in it.
# The directory of 60,000 empty files is told as a count and a checksum of its names:
# listing it twice would only make a difference hard to read.
find . -path ./many -prune -o -type d -printf '%p|d|%m|%U:%G|%T@\n' | sort
find . -path ./many -prune -o ! -type d -printf '%p|%y|%s|%m|%U:%G|%T@|%l\n' | sort
echo "many: $(find many -type f | wc -l) files, names $(find many -type f -printf '%p|%y|%s|%m|%U:%G\n' | sort | sha256sum | cut -d' ' -f1)"
stat -c 'many itself|%a|%u:%g|%Y' many
# Names that are one file, whatever the inode numbers are.
find . -path ./many -prune -o -type f -links +1 -printf '%i\t%p\n' | sort | {
	last=
	line=
	while IFS='	' read -r ino path; do
		if [ "$ino" = "$last" ]; then
			line="$line $path"
		else
			[ -n "$line" ] && echo "one file:$line"
			line=" $path"
			last=$ino
		fi
	done
	[ -n "$line" ] && echo "one file:$line"
} | sort
getcap cap 2>/dev/null
getfattr -d xattr.txt 2>/dev/null
find . -path ./many -prune -o -type f -size -8M -print0 | sort -z | xargs -0 sha256sum
[ -e big.bin ] && sha256sum big.bin
cat /etc/wsl.conf 2>/dev/null
echo end
