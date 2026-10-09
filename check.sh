#!/bin/sh
# 在試還原出來的暫時 distro 裡以 root 執行。
# 先回報這份複本有沒有「活起來」（systemd、Windows 磁碟、互通），再比對備份時記下的檔案雜湊。
# 每行結果以「@wslbak<TAB>」開頭寫到 stdout。
#
# 變數由 wslbak 加在這份腳本前面的賦值提供：
#   WSLBAK_SAMPLES  sha256sum -c 格式的清單（「雜湊  路徑」，路徑相對於 /）
#   WSLBAK_UID      備份當時預設使用者的 UID；空的表示不檢查

say() {
	printf '@wslbak\t%s\n' "$1"
}

main() {
	PATH="$PATH:/run/current-system/sw/bin"
	LC_ALL=C
	export PATH LC_ALL

	say "pid1	$(cat /proc/1/comm 2>/dev/null)"
	if [ -d /run/systemd/system ]; then
		say "systemd	yes"
	else
		say "systemd	no"
	fi
	# Windows 的磁碟掛進來時，掛載來源是 C:\ 這種樣子。
	drives=0
	while read -r source _mount type _rest; do
		if [ "$type" = 9p ]; then
			case $source in
			[A-Za-z]:*) drives=$((drives + 1)) ;;
			esac
		fi
	done </proc/self/mounts
	say "windows-drives	$drives"
	if [ -n "$WSL_INTEROP" ]; then
		say "interop	yes"
	else
		say "interop	no"
	fi
	conf_sum=$(sha256sum /etc/wsl.conf 2>/dev/null)
	say "wslconf	${conf_sum%% *}"

	if [ -n "$WSLBAK_UID" ]; then
		entry=
		while IFS=: read -r name _pw uid _gid _gecos home _shell; do
			if [ "$uid" = "$WSLBAK_UID" ]; then
				entry="$name	$home"
				break
			fi
		done </etc/passwd
		if [ -z "$entry" ]; then
			say "user	missing"
		else
			home=${entry#*	}
			if [ -d "$home" ]; then
				say "user	ok	$entry"
			else
				say "user	no-home	$entry"
			fi
		fi
	fi

	cd / || exit 4
	if [ -n "$WSLBAK_SAMPLES" ]; then
		# 不用 --quiet：busybox 的 sha256sum 沒有這個選項。
		printf '%s\n' "$WSLBAK_SAMPLES" | sha256sum -c 2>/dev/null | {
			ok=0
			bad=0
			while IFS= read -r line; do
				case $line in
				*": OK") ok=$((ok + 1)) ;;
				*)
					bad=$((bad + 1))
					[ "$bad" -le 20 ] && say "sample-bad	$line"
					;;
				esac
			done
			say "samples	$ok	$bad"
		}
	fi
	say "done"
}

main "$@" </dev/null
