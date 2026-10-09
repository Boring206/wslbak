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
	say "windows-drives	$(awk '$3 == "9p" && $1 ~ /^[A-Za-z]:/ { n++ } END { print n + 0 }' /proc/self/mounts)"
	if [ -n "$WSL_INTEROP" ]; then
		say "interop	yes"
	else
		say "interop	no"
	fi
	say "wslconf	$(sha256sum /etc/wsl.conf 2>/dev/null | cut -d' ' -f1)"

	if [ -n "$WSLBAK_UID" ]; then
		entry=$(awk -F: -v uid="$WSLBAK_UID" '$3 == uid { print $1 "\t" $6; exit }' /etc/passwd 2>/dev/null)
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
		printf '%s\n' "$WSLBAK_SAMPLES" | sha256sum -c 2>/dev/null | awk '
			/: OK$/ { ok++; next }
			{ bad++; if (bad <= 20) print "@wslbak\tsample-bad\t" $0 }
			END { printf "@wslbak\tsamples\t%d\t%d\n", ok + 0, bad + 0 }
		'
	fi
	say "done"
}

main "$@" </dev/null
