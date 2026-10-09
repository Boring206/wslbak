#!/bin/sh
# 在 distro 裡以 root 執行，替「只取回某些檔案」做準備與收尾。
#
# 檔案不直接解開到使用者指定的地方，而是先解到根目錄底下一個只有 root 進得去的暫存資料夾，
# 全部解完才整個搬到目的地。目的地的上一層通常屬於一般使用者；直接往那裡解的話，
# 那個使用者可以在解開的途中把路徑換成連結，讓 root 把檔案寫到別的地方去。
# 目的地必須不存在、或是空的資料夾，所以不會蓋掉任何現有的東西。
# 每行結果以「@wslbak<TAB>」開頭寫到 stdout。
#
# 變數由 wslbak 加在這份腳本前面的賦值提供：
#   WSLBAK_STEP    prepare：檢查目的地並建立暫存資料夾；finish：把暫存資料夾搬到目的地
#   WSLBAK_TARGET  目的地，distro 裡的絕對路徑
#   WSLBAK_TOKEN   這次作業的代號（16 個十六進位字元），暫存資料夾的名稱由它決定

say() {
	printf '@wslbak\t%s\n' "$1"
}

prepare() {
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
	elif [ ! -d "$(dirname "$target")" ]; then
		say "error	no-parent"
		exit 3
	fi
	# 之後解開檔案的那個指令不經過 shell，只找得到一般位置上的 tar；
	# 工具放在別處的 distro（例如 NixOS）要先在這裡把它的位置找出來。
	say "tar	$(command -v tar 2>/dev/null)"
	# 不加 -p：已經有同名的東西就失敗，不沿用別人放在那裡的資料夾。
	if ! mkdir -m 700 "$staging" 2>/dev/null; then
		say "error	staging-failed"
		exit 3
	fi
	say "staged	$staging"
}

finish() {
	if [ -L "$staging" ] || [ ! -d "$staging" ]; then
		say "error	staging-missing"
		exit 3
	fi
	# 擁有者在搬過去之前就設好，交給目的地上一層的擁有者，使用者之後才能自己整理或刪除它。
	# 搬過去之後才改的話，改到的可能已經是被換掉的東西。
	chown "$(stat -c '%u:%g' "$(dirname "$target")" 2>/dev/null)" "$staging" 2>/dev/null
	chmod 755 "$staging"
	# -T 把目的地當成「要變成的那個名字」，不是「要搬進去的資料夾」。
	# 目的地這時如果已經被換成連結、檔案或有東西的資料夾，這裡會失敗，而不是跟著走。
	if ! mv -T "$staging" "$target" 2>/dev/null; then
		chown 0:0 "$staging" 2>/dev/null
		chmod 700 "$staging"
		say "error	move-failed"
		exit 3
	fi
}

main() {
	PATH="$PATH:/run/current-system/sw/bin"
	LC_ALL=C
	export PATH LC_ALL
	target=$WSLBAK_TARGET
	staging="/.wslbak-restore-$WSLBAK_TOKEN"

	case $WSLBAK_STEP in
	prepare) prepare ;;
	finish) finish ;;
	*)
		say "error	bad-step"
		exit 3
		;;
	esac
	say "done"
}

main "$@" </dev/null
