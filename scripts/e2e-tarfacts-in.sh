#!/bin/sh
# Runs as root inside the throwaway test distro, started by scripts/e2e-tarfacts.sh.
# It builds a small tree under /wslbak-facts with the kinds of entries that matter for
# differential backups, runs GNU tar over it the three ways wslbak does (the listing pass,
# a full dump, a dump of what changed since a time), and leaves the results in
# /wslbak-facts/out for the script outside to compare. What can be decided here is
# printed as lines "fact<TAB>name<TAB>ok|FAIL|skip<TAB>detail".
LC_ALL=C
export LC_ALL
PATH="$PATH:/run/current-system/sw/bin"

F=/wslbak-facts
TREE=$F/tree
OUT=$F/out

fact() { printf 'fact\t%s\t%s\t%s\n' "$1" "$2" "${3:-}"; }
# ctime with nanoseconds, as one comparable string.
ct() { stat -c %z "$1"; }
mt() { stat -c %y "$1"; }
# changed <name> <before> <after> <expected: yes|no>
changed() {
	if [ "$2" != "$3" ]; then got=yes; else got=no; fi
	if [ "$got" = "$4" ]; then fact "$1" ok "changed=$got"; else fact "$1" FAIL "changed=$got, expected $4"; fi
}
# Some things can only be made with a scripting language; neither is in every image.
script_lang() {
	if command -v python3 >/dev/null 2>&1; then echo python3; elif command -v perl >/dev/null 2>&1; then echo perl; fi
}
make_socket() {
	case $(script_lang) in
	python3) python3 -c 'import socket,sys; s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1])' "$1" ;;
	perl) perl -MIO::Socket::UNIX -e 'IO::Socket::UNIX->new(Local=>$ARGV[0],Listen=>1) or die' "$1" ;;
	*) return 1 ;;
	esac
}

umount "$TREE/alias" "$TREE/tmpfsmnt" "$TREE/vol2" "$TREE/bindfile" 2>/dev/null
rm -rf "$F"
mkdir -p "$TREE" "$OUT"
cd "$TREE" || exit 1

fact tar-version ok "$(tar --version | head -n 1)"
fact kernel ok "$(uname -r)"
while read -r _id _parent _dev _fsroot mount rest; do
	if [ "$mount" = / ]; then
		fs=${rest#* - }
		fact root-filesystem ok "${fs%% *}"
	fi
done </proc/self/mountinfo

# ---- The tree ---------------------------------------------------------------------
mkdir -p a/sub b/sub 'odd dir' excl/keep excl/drop big empty
echo one >a/f1
echo two >a/sub/f2
echo three >b/f1
echo four >b/sub/f3
echo keep >excl/keep/k
echo drop >excl/drop/d
ln a/f1 a/hard
ln -s f1 a/sym
mkfifo a/fifo
mknod a/dev c 1 3
if make_socket a/sock 2>/dev/null; then fact socket-made ok; else fact socket-made skip "no python3 or perl"; fi
: >"$(printf 'new\nline')"
: >'back\slash'
: >"$(printf 'bad\377utf')"
: >'trail '
: >'./-dash'
: >'star*?[x]'
: >'colon:name'
truncate -s 9G big/sparse.bin
printf data | dd of=big/sparse.bin bs=1 seek=1000000 conv=notrunc 2>/dev/null
echo plain >big/plain.bin
mkdir tmpfsmnt vol2
: >bindfile
mount -t tmpfs none tmpfsmnt && echo t >tmpfsmnt/inside
if command -v mkfs.ext4 >/dev/null 2>&1 && truncate -s 32M "$F/vol.img" && mkfs.ext4 -q "$F/vol.img" && mount -o loop "$F/vol.img" vol2; then
	echo v >vol2/inside
	fact second-volume ok
else
	fact second-volume skip "no mkfs.ext4 or no loop device"
fi
if mount --bind /proc/version bindfile; then fact file-bind-mount ok; else fact file-bind-mount skip; fi
cat /proc/self/mountinfo >"$OUT/mountinfo"

# The options every pass shares, and what only a real dump adds.
set -- --directory="$TREE" --format=posix --numeric-owner --one-file-system --anchored \
	'--exclude=./excl/drop' '--exclude=./a/sub/*'
optional=
if tar --create --file=/dev/null --files-from=/dev/null --xattrs --acls 2>/dev/null; then
	optional="--xattrs --acls"
elif tar --create --file=/dev/null --files-from=/dev/null --xattrs 2>/dev/null; then
	optional="--xattrs"
fi
for w in no-xdev rename-directory no-file-ignored; do
	if tar --create --file=/dev/null --files-from=/dev/null "--warning=$w" 2>/dev/null; then
		fact "accepts --warning=$w" ok
	else
		fact "accepts --warning=$w" FAIL
	fi
done

# ---- 1. The listing pass and a full dump of the same tree --------------------------
listing() { # listing <snapshot> <stderr> [more options…]
	snap=$1
	err=$2
	shift 2
	rm -f "$snap"
	: >"$snap"
	tar --create --file=/dev/null --listed-incremental="$snap" "$@" . 2>"$err"
}
listing "$OUT/list.snar" "$OUT/list.err" "$@" --warning=no-xdev
fact listing-exit ok "$?"
fact listing-bytes ok "$(wc -c <"$OUT/list.snar")"
# shellcheck disable=SC2086
tar --create --file="$OUT/dump.tar" "$@" --sparse --totals --warning=no-file-ignored $optional . 2>"$OUT/dump.err"
fact dump-exit ok "$?"

# The same listing without --warning=no-xdev: what does this tar say about mount points?
listing "$OUT/list-xdev.snar" "$OUT/list-xdev.err" "$@"
fact listing-exit-with-xdev-warnings ok "$?"

# ---- 3. The listing pass must not open any regular file ----------------------------
if command -v strace >/dev/null 2>&1; then
	rm -f "$OUT/strace.snar"
	: >"$OUT/strace.snar"
	strace -f -e trace=open,openat -o "$OUT/strace.txt" \
		tar --create --file=/dev/null --listed-incremental="$OUT/strace.snar" "$@" --warning=no-xdev . 2>/dev/null
	opened=0
	for name in f1 plain.bin sparse.bin k hard; do
		while IFS= read -r line; do
			case $line in *"/$name\""* | *"\"$name\""*) opened=$((opened + 1)) ;; esac
		done <"$OUT/strace.txt"
	done
	if [ "$opened" = 0 ]; then fact listing-opens-no-file ok; else fact listing-opens-no-file FAIL "$opened open calls on regular files"; fi
else
	fact listing-opens-no-file skip "no strace"
fi
# Without strace: the 9 GiB sparse file must not slow the listing down.
start=$(date +%s)
listing "$OUT/timing.snar" /dev/null "$@" --warning=no-xdev
took=$(($(date +%s) - start))
if [ "$took" -le 3 ]; then fact listing-is-quick ok "${took}s"; else fact listing-is-quick FAIL "${took}s"; fi
rm -f "$OUT/timing.snar"

# ---- 2. One directory visible at two paths (a bind mount on the same file system) ----
mkdir alias
if mount --bind "$TREE/a" "$TREE/alias"; then
	listing "$OUT/alias.snar" "$OUT/alias.err" "$@" --warning=no-xdev
	fact alias-listing-exit ok "$?"
	if tar --create --file=/dev/null --files-from=/dev/null --warning=rename-directory 2>/dev/null; then
		listing "$OUT/alias-warn.snar" "$OUT/alias-warn.err" "$@" --warning=no-xdev --warning=rename-directory
	fi
	cat /proc/self/mountinfo >"$OUT/alias.mountinfo"
	# shellcheck disable=SC2086
	tar --create --file="$OUT/alias-dump.tar" "$@" --sparse --totals --warning=no-file-ignored $optional . 2>"$OUT/alias-dump.err"
	umount "$TREE/alias"
else
	fact alias-listing-exit skip "mount --bind failed"
fi
rmdir alias

# ---- 5. What changes an inode's ctime on this kernel's ext4 ------------------------
mkdir -p ct/d1/inner ct/d2 ct/p1 ct/p2
for f in ren old over victim keep mm rd ch x1 x2; do echo "$f" >"ct/$f"; done
echo deep >ct/d1/inner/file
ln ct/victim ct/victim-link
sleep 1.1

b=$(ct ct/ren); mv ct/ren ct/ren2; changed "ctime: rename a file" "$b" "$(ct ct/ren2)" yes
bd=$(ct ct/d1); bf=$(ct ct/d1/inner/file); bi=$(ct ct/d1/inner)
mv ct/d1 ct/d1moved
changed "ctime: rename a directory (the directory itself)" "$bd" "$(ct ct/d1moved)" yes
changed "ctime: rename a directory (a file below it)" "$bf" "$(ct ct/d1moved/inner/file)" no
changed "ctime: rename a directory (a directory below it)" "$bi" "$(ct ct/d1moved/inner)" no
sleep 1.1
bd=$(ct ct/d1moved); mv ct/d1moved ct/p1/d1; changed "ctime: move a directory to another parent" "$bd" "$(ct ct/p1/d1)" yes
if mv --exchange -T ct/x1 ct/x2 2>/dev/null; then
	fact "ctime: exchange is available" ok
	mkdir ct/e1 ct/e2; echo 1 >ct/e1/f; echo 2 >ct/e2/f; sleep 1.1
	b1=$(ct ct/e1); b2=$(ct ct/e2); bf=$(ct ct/e1/f)
	mv --exchange -T ct/e1 ct/e2
	changed "ctime: exchange two directories (first)" "$b2" "$(ct ct/e1)" yes
	changed "ctime: exchange two directories (second)" "$b1" "$(ct ct/e2)" yes
	changed "ctime: exchange two directories (a file below)" "$bf" "$(ct ct/e2/f)" no
else
	fact "ctime: exchange is available" skip "mv has no --exchange"
fi
b=$(ct ct/keep); ln ct/keep ct/keep-link; changed "ctime: add a hard link" "$b" "$(ct ct/keep)" yes
sleep 1.1
b=$(ct ct/keep); rm ct/keep-link; changed "ctime: remove one of two links" "$b" "$(ct ct/keep)" yes
b=$(ct ct/victim-link); mv ct/over ct/victim; changed "ctime: a name of the inode is renamed over" "$b" "$(ct ct/victim-link)" yes
sleep 1.1
b=$(ct ct/old); touch -d '2001-01-01 00:00:00 UTC' ct/old; changed "ctime: set the modification time into the past" "$b" "$(ct ct/old)" yes
b=$(ct ct/ch); chmod 600 ct/ch; changed "ctime: chmod" "$b" "$(ct ct/ch)" yes
b=$(ct ct/rd); bm=$(mt ct/rd); cat ct/rd >/dev/null; sleep 1.1; cat ct/rd >/dev/null
changed "ctime: reading a file" "$b" "$(ct ct/rd)" no
changed "mtime: reading a file" "$bm" "$(mt ct/rd)" no
if command -v python3 >/dev/null 2>&1; then
	b=$(ct ct/mm)
	python3 -c 'import mmap,sys
f=open(sys.argv[1],"r+b"); m=mmap.mmap(f.fileno(),0); m[0:1]=b"X"; m.flush(); m.close(); f.close()' ct/mm
	sync
	changed "ctime: a write through mmap" "$b" "$(ct ct/mm)" yes
else
	fact "ctime: a write through mmap" skip "no python3"
fi
bp=$(ct ct/p2); bpm=$(mt ct/p2); echo new >ct/p2/new; changed "ctime: a directory that gains an entry" "$bp" "$(ct ct/p2)" yes
changed "mtime: a directory that gains an entry" "$bpm" "$(mt ct/p2)" yes
sleep 1.1
bp=$(ct ct/p2); echo more >>ct/p2/new; changed "ctime: a directory whose file is rewritten in place" "$bp" "$(ct ct/p2)" no

# ---- 6. A dump of what changed since a time ---------------------------------------
mkdir -p since/keepdir/deep since/work
for f in untouched edited chmodded backdated renamed linked; do echo "$f" >"since/work/$f"; done
echo still >since/keepdir/deep/still
sleep 2
T=$(date +%s)
echo "$T" >"$OUT/since.T"
sleep 2
echo more >>since/work/edited
chmod 640 since/work/chmodded
touch -d '2001-01-01 00:00:00 UTC' since/work/backdated
mv since/work/renamed since/work/renamed2
ln since/work/linked since/work/linked2
echo fresh >since/work/created
# shellcheck disable=SC2086
if tar --create --file="$OUT/since.tar" "$@" --sparse --totals --warning=no-file-ignored $optional "--after-date=@$T" . 2>"$OUT/since.err"; then
	fact after-date-exit ok 0
else
	fact after-date-exit ok "$?"
fi
find . -xdev -type d ! -path './excl/drop' ! -path './excl/drop/*' | sort >"$OUT/since.dirs"

# ---- 8. A change stamped earlier than the base time ----------------------------------
# The clock is shared by every distro on this PC, so it is not touched. Asking for what
# changed since a time one hour ahead is the same situation: a real change, stamped
# earlier than the time the dump is asked about.
mkdir clock
echo before >clock/file
echo other >clock/other
stat -c '%n|%z' clock >"$OUT/clock.before"
sleep 1.1
echo after >clock/file
rm clock/other
AHEAD=$(($(date +%s) + 3600))
echo "$AHEAD" >"$OUT/clock.T"
stat -c '%n|%z|%Z' clock >"$OUT/clock.after"
# shellcheck disable=SC2086
tar --create --file="$OUT/clock.tar" "$@" --sparse --totals --warning=no-file-ignored $optional "--after-date=@$AHEAD" . 2>"$OUT/clock.err"

umount "$TREE/bindfile" "$TREE/vol2" "$TREE/tmpfsmnt" 2>/dev/null
echo finished
