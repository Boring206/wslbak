#!/bin/sh
# 在 distro 裡以 root 執行，替「只取回某些檔案」準備好目的地。
# 目的地必須不存在、或是空的資料夾：取回的檔案只會放進全新的地方，不會蓋掉任何現有的東西。
# 每行結果以「@wslbak<TAB>」開頭寫到 stdout。
#
# 變數由 wslbak 加在這份腳本前面的賦值提供：
#   WSLBAK_TARGET  目的地，distro 裡的絕對路徑
#   WSLBAK_TOKEN   這次作業的代號（16 個十六進位字元）
#
# 準備好之後，在記憶體檔案系統上建立一個以代號命名的連結指向目的地。
# 之後解開檔案的那個指令只需要用到這個連結，使用者給的路徑就不必出現在任何命令列上。

say() {
	printf '@wslbak\t%s\n' "$1"
}

main() {
	PATH="$PATH:/run/current-system/sw/bin"
	LC_ALL=C
	export PATH LC_ALL
	target=$WSLBAK_TARGET

	case $target in
	/*) ;;
	*)
		say "error	not-absolute"
		exit 3
		;;
	esac

	if [ -e "$target" ] || [ -L "$target" ]; then
		if [ -L "$target" ] || [ ! -d "$target" ]; then
			say "error	not-a-directory"
			exit 3
		fi
		if [ -n "$(ls -A "$target" 2>/dev/null)" ]; then
			say "error	not-empty"
			exit 3
		fi
	else
		parent=$(dirname "$target")
		if [ ! -d "$parent" ]; then
			say "error	no-parent"
			exit 3
		fi
		if ! mkdir "$target"; then
			say "error	mkdir-failed"
			exit 3
		fi
		# 新資料夾交給上層資料夾的擁有者，使用者之後才能自己整理或刪除它。
		chown "$(stat -c '%u:%g' "$parent")" "$target" 2>/dev/null
	fi

	for base in /run /dev/shm /tmp; do
		if [ -d "$base" ] && [ -w "$base" ]; then
			link="$base/wslbak-$WSLBAK_TOKEN"
			if ln -s "$target" "$link" 2>/dev/null; then
				say "staged	$link"
				say "done"
				exit 0
			fi
		fi
	done
	say "error	no-staging"
	exit 3
}

main "$@" </dev/null
