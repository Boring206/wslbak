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

	tar_version=$(tar --version 2>/dev/null | head -n 1)
	case $tar_version in
	*"GNU tar"*) say "tar	gnu	$tar_version" ;;
	"") say "tar	missing" ;;
	*) say "tar	other	$tar_version" ;;
	esac

	# 根檔案系統已使用的空間，用來估計備份與試還原需要多少空間。
	say "used-kb	$(df -Pk / 2>/dev/null | awk 'NR == 2 { print $3 }')"
	say "os	$(. /etc/os-release 2>/dev/null && printf '%s' "$PRETTY_NAME")"
	if command -v sha256sum >/dev/null 2>&1; then
		say "sha256sum	yes"
	else
		say "sha256sum	no"
	fi
	say "done"
}

main "$@" </dev/null
