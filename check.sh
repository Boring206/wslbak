#!/bin/sh
# 在試還原出來的暫時 distro 裡以 root 執行。
# 先回報這份複本有沒有「活起來」（systemd、Windows 磁碟、互通），再比對備份時記下的檔案雜湊。
# 每行結果以「@wslbak<TAB>」開頭寫到 stdout。
#
# 變數由 wslbak 加在這份腳本前面的賦值提供：
#   WSLBAK_SAMPLES  sha256sum -c 格式的清單（「雜湊  路徑」，路徑相對於 /）
#   WSLBAK_SIZES    最大的幾個檔案，一行一個「大小<TAB>路徑」；只比大小，不讀內容
#   WSLBAK_UID      備份當時預設使用者的 UID；空的表示不檢查

say() {
	printf '@wslbak\t%s\n' "$1"
}

# rewritten 回報一個路徑（「./」開頭）是不是 WSL 啟動 distro 時自己改寫的檔案（見下面的 volatile）。
rewritten() {
	[ -n "$1" ] || return 1
	case "
$volatile
" in
	*"
$1
"*) return 0 ;;
	esac
	return 1
}

main() {
	# NixOS 的工具不在一般的位置。/run/current-system 是開機時才建立的，
	# 這份複本沒有開過機，所以也從磁碟上的系統設定檔去找。
	PATH="$PATH:/run/current-system/sw/bin:/nix/var/nix/profiles/system/sw/bin"
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
	# WSL 每次啟動 distro 都把主機名稱寫進 /etc/hostname，這份複本也一樣；寫進去的是這台電腦的名稱，
	# 因為原本 wsl.conf 裡的 hostname 設定沒有帶過來。/etc/hosts 與 /etc/resolv.conf 在這份複本裡
	# 已經關掉不重寫，列著只是保險。這幾個名稱本身不會被抽樣；但它們是符號連結時（NixOS 把
	# /etc/hostname 連進 /nix/store），被改寫的是連結指向的那個檔案，而那個檔案可能被抽到。
	# 這裡找出它們在這份複本裡真正的位置：那些檔案內容不同，不代表還原出了問題。
	volatile=
	for f in /etc/hostname /etc/hosts /etc/resolv.conf; do
		real=$(readlink -f "$f" 2>/dev/null)
		if [ -n "$real" ]; then
			volatile="$volatile
.$real"
		fi
	done
	if [ -n "$WSLBAK_SAMPLES" ]; then
		# 不用 --quiet：busybox 的 sha256sum 沒有這個選項。
		printf '%s\n' "$WSLBAK_SAMPLES" | sha256sum -c 2>/dev/null | {
			ok=0
			bad=0
			skipped=0
			while IFS= read -r line; do
				case $line in
				*": OK") ok=$((ok + 1)) ;;
				*)
					if rewritten "${line%: FAILED*}"; then
						skipped=$((skipped + 1))
					else
						bad=$((bad + 1))
						[ "$bad" -le 20 ] && say "sample-bad	$line"
					fi
					;;
				esac
			done
			say "samples	$ok	$bad	$skipped"
		}
	fi
	if [ -n "$WSLBAK_SIZES" ]; then
		printf '%s\n' "$WSLBAK_SIZES" | {
			ok=0
			bad=0
			while IFS='	' read -r want path; do
				[ -n "$path" ] || continue
				got=$(stat -c %s "$path" 2>/dev/null)
				if [ "$got" = "$want" ]; then
					ok=$((ok + 1))
				else
					bad=$((bad + 1))
					say "size-bad	$path	expected $want, found ${got:-nothing}"
				fi
			done
			say "sizes	$ok	$bad"
		}
	fi
	say "done"
}

main "$@" </dev/null
