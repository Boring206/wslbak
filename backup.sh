#!/bin/sh
# 在 distro 裡以 root 執行：把整個根檔案系統打包成 tar，寫到 stdout。
# stdout 只有 tar 的原始資料；診斷訊息寫到 stderr，每行以「@wslbak<TAB>」開頭。
# stderr 上沒有這個前綴的行是 tar 自己的訊息（固定用英文，由 wslbak 判讀）。
#
# 變數由 wslbak 加在這份腳本前面的賦值提供：
#   WSLBAK_ROOT      要打包的根目錄，預設 /
#   WSLBAK_EXCLUDES  要排除的樣式，一行一個，寫法同 tar 的 --exclude

say() {
	printf '@wslbak\t%s\n' "$1" >&2
}

main() {
	# NixOS 的工具不在預設的 PATH 上。
	PATH="$PATH:/run/current-system/sw/bin"
	LC_ALL=C
	export PATH LC_ALL

	root=${WSLBAK_ROOT:-/}

	tar_version=$(tar --version 2>/dev/null | head -n 1)
	case $tar_version in
	*"GNU tar"*) ;;
	*)
		say "error	not-gnu-tar	$tar_version"
		exit 3
		;;
	esac
	say "tar-version	$tar_version"

	# 不是每個檔案系統與每個 tar 的編譯選項都支援 ACL 與延伸屬性：先空跑一次確認，不行就依序拿掉。
	optional=
	if tar --create --file=/dev/null --files-from=/dev/null --xattrs --acls 2>/dev/null; then
		optional="--xattrs --acls"
	elif tar --create --file=/dev/null --files-from=/dev/null --xattrs 2>/dev/null; then
		optional="--xattrs"
		say "dropped	--acls"
	else
		say "dropped	--acls"
		say "dropped	--xattrs"
	fi

	# --one-file-system 會跳過掛在別的磁碟上的目錄。那裡如果是真正的資料（例如獨立的 /home），
	# 使用者得知道它沒有被備份。和根目錄同一個裝置的掛載點（綁定掛載）不算。
	if [ "$root" = / ] && [ -r /proc/self/mountinfo ]; then
		awk '
			{
				fs = ""
				for (i = 7; i <= NF; i++) if ($i == "-") { fs = $(i + 1); break }
				if ($5 == "/") { rootdev = $3; next }
				if (fs ~ /^(ext[234]|xfs|btrfs|f2fs|zfs|jfs|reiserfs|bcachefs)$/) { dev[$5] = $3; type[$5] = fs }
			}
			END {
				for (m in dev) if (dev[m] != rootdev && m !~ /^\/mnt\/wslg?(\/|$)/) printf "@wslbak\tskipped-mount\t%s\t%s\n", m, type[m]
			}
		' /proc/self/mountinfo >&2
	fi

	set -- --create --file=- --directory="$root" --format=posix --numeric-owner \
		--one-file-system --sparse --totals --warning=no-file-ignored --anchored
	# 樣式裡有 *，展開變數時要關掉檔名展開。
	set -f
	old_ifs=$IFS
	IFS='
'
	for pattern in $WSLBAK_EXCLUDES; do
		set -- "$@" "--exclude=$pattern"
	done
	IFS=$old_ifs
	# shellcheck disable=SC2086
	set -- "$@" $optional
	set +f

	tar "$@" .
	status=$?
	say "tar-exit	$status"
	say "done"
	exit "$status"
}

main "$@" </dev/null
