#!/bin/sh
# 在 distro 裡以 root 執行，回報備份前要知道的事。每行以「@wslbak<TAB>」開頭寫到 stdout。

say() {
	printf '@wslbak\t%s\n' "$1"
}

main() {
	# NixOS 的工具不在預設的 PATH 上。
	PATH="$PATH:/run/current-system/sw/bin"
	LC_ALL=C
	export PATH LC_ALL

	tar_version=$(tar --version 2>/dev/null)
	tar_version=${tar_version%%
*}
	case $tar_version in
	*"GNU tar"*) say "tar	gnu	$tar_version" ;;
	"") say "tar	missing" ;;
	*) say "tar	other	$tar_version" ;;
	esac

	# 根檔案系統已使用的空間，用來估計備份與試還原需要多少空間。
	# df -P 的第二行：檔案系統 總區塊 已使用 可用 …。不用 awk，有些精簡的 distro 沒有。
	used=$(df -Pk / 2>/dev/null | {
		read -r _header
		read -r _fs _blocks used _rest
		printf '%s' "$used"
	})
	say "used-kb	$used"
	say "os	$(. /etc/os-release 2>/dev/null && printf '%s' "$PRETTY_NAME")"
	if command -v sha256sum >/dev/null 2>&1; then
		say "sha256sum	yes"
	else
		say "sha256sum	no"
	fi
	say "done"
}

main "$@" </dev/null
