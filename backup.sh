#!/bin/sh
# 在 distro 裡以 root 執行。三種模式（WSLBAK_MODE）：
#   full  把整個根檔案系統打包成 tar，寫到 stdout（預設）
#   diff  同上，但檔案只收內容或屬性在 WSLBAK_SINCE 之後變過的；目錄一律全收
#   list  不打包：讓 tar 把同一棵樹空跑一遍，把它的快照檔寫到 stdout。
#         快照檔列出每個目錄的 inode 與底下的名稱；差異備份靠它知道現在有哪些檔案、
#         哪個目錄還是原來那個。
# stdout 只有 tar（或快照檔）的原始資料；診斷訊息寫到 stderr，每行以「@wslbak<TAB>」開頭。
# stderr 上沒有這個前綴的行是 tar 自己的訊息（固定用英文，由 wslbak 判讀）。
#
# 變數由 wslbak 加在這份腳本前面的賦值提供：
#   WSLBAK_ROOT      要打包的根目錄，預設 /
#   WSLBAK_EXCLUDES  要排除的樣式，一行一個，寫法同 tar 的 --exclude
#   WSLBAK_MODE      見上
#   WSLBAK_SINCE     diff 模式的基準時間：從 1970 年起算的秒數

say() {
	printf '@wslbak\t%s\n' "$1" >&2
}

# first_line 印出一段文字的第一行。
first_line() {
	printf '%s\n' "${1%%
*}"
}

# accepts 回報這個 tar 認不認得某幾個選項：拿它們空跑一次。
accepts() {
	tar --create --file=/dev/null --files-from=/dev/null "$@" 2>/dev/null
}

# scratch_dir 找一個在記憶體裡、而且是掛載點本身的資料夾，印出它的路徑。
# 快照檔只能是一般的檔案（tar 要回頭改寫它），但不該寫到 distro 的磁碟上。
scratch_dir() {
	for dir in /run /dev/shm; do
		while read -r _id _parent _dev _fsroot mount rest; do
			fs=${rest#* - }
			if [ "$mount" = "$dir" ] && [ "${fs%% *}" = tmpfs ] && [ -d "$dir" ] && [ -w "$dir" ]; then
				printf '%s\n' "$dir"
				return 0
			fi
		done </proc/self/mountinfo
	done
	return 1
}

main() {
	# NixOS 的工具不在預設的 PATH 上。
	PATH="$PATH:/run/current-system/sw/bin"
	LC_ALL=C
	export PATH LC_ALL

	root=${WSLBAK_ROOT:-/}
	mode=${WSLBAK_MODE:-full}

	tar_version=$(first_line "$(tar --version 2>/dev/null)")
	case $tar_version in
	*"GNU tar"*) ;;
	*)
		say "error	not-gnu-tar	$tar_version"
		exit 3
		;;
	esac
	say "tar-version	$tar_version"
	# 檔案的時間是這裡的時鐘寫的，不是 Windows 的。
	say "clock	$(date +%s)"

	if [ -r /proc/self/mountinfo ]; then
		# 每一行：編號 上層 裝置 根 掛載點 選項… - 檔案系統類型 來源 …
		# 只用 shell 自己的功能來拆：有些精簡的 distro（例如 Fedora 的 WSL 映像）沒有 awk。
		# 全部原樣回報，由 wslbak 判斷哪些掛載點在這棵樹裡、有沒有同一個目錄出現在兩個地方。
		rootdev=
		while read -r id parent dev fsroot mount rest; do
			[ "$mount" = / ] && rootdev=$dev
			fs=${rest#* - }
			say "mount	$id	$parent	$dev	$fsroot	$mount	${fs%% *}"
		done </proc/self/mountinfo
		# --one-file-system 會跳過掛在別的磁碟上的目錄。那裡如果是真正的資料（例如獨立的 /home），
		# 使用者得知道它沒有被備份。和根目錄同一個裝置的掛載點（綁定掛載）不算。
		if [ "$root" = / ]; then
			while read -r _id _parent dev _fsroot mount rest; do
				fs=${rest#* - }
				fs=${fs%% *}
				case $fs in
				ext2 | ext3 | ext4 | xfs | btrfs | f2fs | zfs | jfs | reiserfs | bcachefs) ;;
				*) continue ;;
				esac
				[ "$mount" = / ] && continue
				[ "$dev" = "$rootdev" ] && continue
				case $mount in
				/mnt/wsl | /mnt/wsl/* | /mnt/wslg | /mnt/wslg/*) continue ;;
				esac
				say "skipped-mount	$mount	$fs"
			done </proc/self/mountinfo
		fi
	fi

	# 三種模式走的是同一棵樹：這幾個選項決定「哪些東西算在裡面」，一定要一樣。
	set -- --create --directory="$root" --format=posix --numeric-owner --one-file-system --anchored
	# 樣式裡有 *，展開變數時要關掉檔名展開。
	set -f
	old_ifs=$IFS
	IFS='
'
	for pattern in $WSLBAK_EXCLUDES; do
		set -- "$@" "--exclude=$pattern"
	done
	IFS=$old_ifs
	set +f

	if [ "$mode" = list ]; then
		scratch=$(scratch_dir) || {
			say "error	no-scratch"
			exit 4
		}
		# 上一次被中斷時留下的快照檔。
		rm -f "$scratch"/wslbak-list.??????
		snapshot=$(mktemp "$scratch/wslbak-list.XXXXXX") || {
			say "error	no-scratch"
			exit 4
		}
		# 掛在別的磁碟上的目錄，新版的 tar 每個都會說一聲；那不是問題。
		# 同一個目錄從兩條路徑都走得到時，要它說出來：那種清單不能用。
		for warning in no-xdev rename-directory; do
			if accepts "--warning=$warning"; then
				set -- "$@" "--warning=$warning"
			fi
		done
		# 寫到 /dev/null 時 tar 不開啟也不讀取任何檔案，只看每個項目的資訊。
		tar "$@" --file=/dev/null --listed-incremental="$snapshot" .
		status=$?
		say "snapshot-bytes	$(($(wc -c <"$snapshot")))"
		cat "$snapshot"
		rm -f "$snapshot"
		say "tar-exit	$status"
		say "done"
		exit "$status"
	fi

	if [ "$mode" = diff ]; then
		case $WSLBAK_SINCE in
		'' | *[!0-9]*)
			say "error	bad-since"
			exit 4
			;;
		esac
		set -- "$@" "--after-date=@$WSLBAK_SINCE"
	fi

	# 不是每個檔案系統與每個 tar 的編譯選項都支援 ACL 與延伸屬性：先空跑一次確認，不行就依序拿掉。
	optional=
	if accepts --xattrs --acls; then
		optional="--xattrs --acls"
	elif accepts --xattrs; then
		optional="--xattrs"
		say "dropped	--acls"
	else
		say "dropped	--acls"
		say "dropped	--xattrs"
	fi

	# shellcheck disable=SC2086
	tar "$@" --file=- --sparse --totals --warning=no-file-ignored $optional .
	status=$?
	say "tar-exit	$status"
	say "done"
	exit "$status"
}

main "$@" </dev/null
