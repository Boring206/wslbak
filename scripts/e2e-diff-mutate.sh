#!/bin/sh
# Runs as root inside the throwaway test distro, from the "Differential backups" section of
# scripts/e2e.sh. It plants a tree under /wslbak-diff and then changes it, one step at a
# time, in the ways a differential backup has to follow, and in the ways it must refuse to
# follow. STEP says which; it is set in front of this script.
#
#   plant    the tree as it is when the full backup is made
#   change   everything a differential copes with: edits, deletions, new files, a renamed
#            file, mode, owner, capability and attribute changes, hard links made and
#            broken, a tree deleted and made again, a file that became a directory and a
#            directory that became a file, a time set back, an odd name, /etc/wsl.conf,
#            and a directory of 60,000 files
#   tick     one small change
#   rename   an old directory gets another name (the files in it do not change)
#   swap     two directories with the same names in them change places
#   big      a file of BIG_BYTES bytes that does not compress
#   clean    remove everything again
set -e
LC_ALL=C
export LC_ALL
D=/wslbak-diff

case "${STEP:-}" in
plant)
	rm -rf "$D"
	mkdir -p "$D/keep/sub" "$D/tree-recreated/b" "$D/dir-to-file" "$D/many" "$D/old-dir/sub" "$D/swap-1" "$D/swap-2" "$D/linked-out"
	cd "$D"
	echo "never touched" >keep/unchanged.txt
	touch -d '2020-02-03 04:05:06.123456789 UTC' keep/unchanged.txt
	echo deep >keep/sub/deep.txt
	chmod 750 keep/sub
	chown 4321:8765 keep/sub
	for f in edited deleted renamed-from chmod chown xattr backdated file-to-dir; do echo "$f before" >"$f.txt"; done
	mv file-to-dir.txt file-to-dir
	cp "$(command -v ls)" cap
	if command -v setcap >/dev/null 2>&1; then setcap cap_net_raw+ep cap; fi
	if command -v setfattr >/dev/null 2>&1; then setfattr -n user.wslbak -v before xattr.txt; fi
	echo linked >hard-a
	ln hard-a hard-b
	echo split >split-a
	ln split-a split-b
	echo a >tree-recreated/a
	echo c >tree-recreated/b/c
	echo x >dir-to-file/x
	(cd many && seq 1 60000 | sed 's/^/f/' | xargs touch)
	printf 'before\n' >"$(printf 'name with\nnewline')"
	echo f1 >old-dir/f1
	echo f2 >old-dir/f2
	echo f3 >old-dir/sub/f3
	echo target >linked-out/target
	ln linked-out/target old-dir/link
	echo "first one" >swap-1/x
	echo "the other" >swap-2/x
	sync
	;;
change)
	cd "$D"
	echo more >>edited.txt
	rm deleted.txt
	echo new >added.txt
	mkdir -p newdir/inner
	echo n >newdir/inner/file
	chmod 700 newdir
	mv renamed-from.txt renamed-to.txt
	chmod 600 chmod.txt
	chown 1234:5678 chown.txt
	if command -v setcap >/dev/null 2>&1; then setcap cap_net_admin+ep cap; fi
	if command -v setfattr >/dev/null 2>&1; then setfattr -n user.wslbak -v after xattr.txt; fi
	ln hard-a hard-c
	rm split-a
	rm -r tree-recreated
	mkdir -p tree-recreated/b
	echo again >tree-recreated/a
	echo c2 >tree-recreated/b/c
	rm file-to-dir
	mkdir file-to-dir
	echo inside >file-to-dir/inside
	rm -r dir-to-file
	echo "now a file" >dir-to-file
	echo changed >backdated.txt
	touch -d '2001-01-01 00:00:00 UTC' backdated.txt
	echo one-more >many/extra
	rm many/f1
	printf 'after\n' >"$(printf 'name with\nnewline')"
	# Not where that file cannot be written to (NixOS manages it).
	if [ -w /etc/wsl.conf ] || [ ! -e /etc/wsl.conf ]; then echo '# wslbak e2e' >>/etc/wsl.conf || true; fi
	sync
	;;
tick)
	date +%s%N >"$D/tick"
	;;
rename)
	mv "$D/old-dir" "$D/old-dir-renamed"
	;;
swap)
	mv "$D/swap-1" "$D/swap-tmp"
	mv "$D/swap-2" "$D/swap-1"
	mv "$D/swap-tmp" "$D/swap-2"
	;;
big)
	head -c "${BIG_BYTES:-1}" /dev/urandom >"$D/big.bin"
	sync
	;;
clean)
	rm -rf "$D"
	if [ -w /etc/wsl.conf ]; then
		kept=$(grep -v '^# wslbak e2e$' /etc/wsl.conf || true)
		if [ -n "$kept" ]; then printf '%s\n' "$kept" >/etc/wsl.conf; else rm -f /etc/wsl.conf; fi
	fi
	;;
*)
	echo "unknown step: ${STEP:-none}" >&2
	exit 2
	;;
esac
echo "done ${STEP}"
