#!/bin/sh
# 在 distro 裡以 root 執行，量出常見的「可以重新下載」的快取各佔多少空間。
# 每個存在的資料夾一行：「@wslbak<TAB>cache<TAB>位元組數<TAB>排除樣式<TAB>實際路徑」，寫到 stdout。
# 只讀不寫。du 加上 -x，不會跨到別的檔案系統。

say() {
	printf '@wslbak\t%s\n' "$1"
}

# measure <排除樣式> <資料夾>…：把這些資料夾的大小加總後回報。
measure() {
	pattern=$1
	shift
	total=0
	first=
	for dir in "$@"; do
		[ -d "$dir" ] || continue
		kb=$(du -sxk "$dir" 2>/dev/null | cut -f1)
		case $kb in
		'' | *[!0-9]*) continue ;;
		esac
		total=$((total + kb))
		[ -n "$first" ] || first=$dir
	done
	[ "$total" -gt 0 ] && say "cache	$((total * 1024))	$pattern	$first"
	return 0
}

main() {
	PATH="$PATH:/run/current-system/sw/bin"
	LC_ALL=C
	export PATH LC_ALL

	# 套件管理員下載的安裝檔。
	measure './var/cache/apt/archives/*' /var/cache/apt/archives
	measure './var/cache/pacman/pkg/*' /var/cache/pacman/pkg
	measure './var/cache/dnf/*' /var/cache/dnf
	measure './var/cache/zypp/*' /var/cache/zypp
	measure './var/cache/apk/*' /var/cache/apk

	# 各使用者家目錄裡，開發工具的下載快取。
	measure './home/*/.npm/_cacache/*' /home/*/.npm/_cacache
	measure './home/*/.local/share/pnpm/store/*' /home/*/.local/share/pnpm/store
	measure './home/*/.cache/yarn/*' /home/*/.yarn/cache
	measure './home/*/.gradle/caches/*' /home/*/.gradle/caches
	measure './home/*/.cargo/registry/*' /home/*/.cargo/registry
	measure './home/*/go/pkg/mod/*' /home/*/go/pkg/mod
	measure './home/*/.nuget/packages/*' /home/*/.nuget/packages

	# Docker 的資料只回報大小：映像可以重新下載，volume 裡的資料不行。
	if [ -d /var/lib/docker ]; then
		kb=$(du -sxk /var/lib/docker 2>/dev/null | cut -f1)
		case $kb in
		'' | *[!0-9]*) ;;
		*) say "docker	$((kb * 1024))" ;;
		esac
	fi
	say "done"
}

main "$@" </dev/null
