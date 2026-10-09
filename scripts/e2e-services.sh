#!/usr/bin/env bash
# What happens to software that is busy while the backup runs: a Docker Engine with a
# running container, and a database that is being written to. Slow (it installs Docker in
# a throwaway distro), so it is not part of npm run e2e.
#
#   bash scripts/e2e-services.sh
#
# Docker is started with networking features switched off (no bridge, no firewall rules):
# all WSL2 distros share one network stack, and the test must not change it.
set -u

cd "$(dirname "$0")/.."
E2E_DISTRO="${E2E_DISTRO:-Debian}"
# shellcheck source=scripts/e2e-distro.sh
. scripts/e2e-distro.sh
STATE="$ROOT/local/e2e-distro-services"

[ -n "${WSL_DISTRO_NAME:-}" ] || { echo "Run this inside WSL."; exit 2; }
[ -f bin/wslbak-x64.exe ] || [ -f bin/wslbak-arm64.exe ] || { echo "The executables are missing; run npm run build first."; exit 2; }

RUN_ID="$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
SANDBOX_WIN="$(sandbox_win)\\services-$RUN_ID"
SANDBOX="$(wslpath -u "$SANDBOX_WIN")"
HOME_DIR="$SANDBOX/home"
DEST="$SANDBOX/dest"
RUN_LIMIT=1800
RESTORED="wslbak-e2e-s-$RUN_ID"
RESTORED_DIR="$SANDBOX_WIN\\restored"
# shellcheck source=scripts/e2e-lib.sh
. scripts/e2e-lib.sh

DOCKERD='setsid nohup dockerd --iptables=false --ip6tables=false --ip-forward=false --ip-masq=false --bridge=none >/var/log/dockerd.log 2>&1 &
i=0
while [ $i -lt 60 ] && ! docker info >/dev/null 2>&1; do sleep 1; i=$((i + 1)); done
docker info >/dev/null 2>&1 && echo up'

cleanup() {
	local d
	for d in "${DISTRO:-}" "$RESTORED"; do
		[ -n "$d" ] && registered "$d" && sh_in "$d" 'pkill dockerd; pkill -f churn.sh; true' >/dev/null 2>&1
	done
	[ -d "$HOME_DIR" ] && timeout 120 "${WB[@]}" uninstall --yes </dev/null >/dev/null 2>&1
	wsl_exe --terminate "$RESTORED" </dev/null >/dev/null 2>&1
	unregister_guarded "$RESTORED" "$RESTORED_DIR" 2>/dev/null
	if wsl_exe -l -q </dev/null | tr -d '\r' | grep -q "wslbak-.*$RUN_ID"; then
		echo "a test distro of this run is still registered; leaving $SANDBOX_WIN in place" >&2
	else
		rm -rf "$SANDBOX"
	fi
	[ -z "${KEEP_E2E_DISTRO:-}" ] && [ -s "$STATE" ] && destroy >/dev/null
}
trap cleanup EXIT

section "Test distro with Docker and SQLite"
[ -s "$STATE" ] && destroy >/dev/null
DISTRO="$(create 2>/dev/null)" || { echo "Could not create the test distro (is there network access?)."; exit 2; }
ok "created $DISTRO"
# policy-rc.d keeps the package from starting the Docker service by itself with its default
# network settings.
INSTALLED="$(sh_in "$DISTRO" '
export DEBIAN_FRONTEND=noninteractive
printf "#!/bin/sh\nexit 101\n" >/usr/sbin/policy-rc.d
chmod +x /usr/sbin/policy-rc.d
apt-get update -qq >/dev/null 2>&1
apt-get install -y -qq docker.io sqlite3 >/dev/null 2>&1
command -v dockerd >/dev/null && command -v sqlite3 >/dev/null && echo installed
' | tail -n 1)"
if [ "$INSTALLED" != installed ]; then
	echo "Could not install Docker and SQLite in the test distro."
	exit 2
fi
ok "installed Docker Engine and SQLite"
expect_true "the Docker daemon starts" [ "$(sh_in "$DISTRO" "$DOCKERD" | tail -n 1)" = up ]
STARTED="$(sh_in "$DISTRO" '
docker pull -q busybox >/dev/null 2>&1
docker run -d --network none --name busy busybox sh -c "while true; do date >>/log.txt; sleep 0.2; done" >/dev/null 2>&1
docker ps --format "{{.Names}}" | head -n 1
' | tail -n 1)"
expect_true "a container is running" [ "$STARTED" = busy ]

# Two databases that are written to for the whole length of the backup: one in WAL mode,
# one with a rollback journal.
sh_in "$DISTRO" '
mkdir -p /srv/db
sqlite3 /srv/db/wal.db "PRAGMA journal_mode=WAL; CREATE TABLE t(i INTEGER PRIMARY KEY, x TEXT);" >/dev/null
sqlite3 /srv/db/journal.db "PRAGMA journal_mode=DELETE; CREATE TABLE t(i INTEGER PRIMARY KEY, x TEXT);" >/dev/null
cat >/srv/db/churn.sh <<"CHURN"
i=0
while [ $i -lt 200000 ]; do
	sqlite3 /srv/db/wal.db "INSERT INTO t(x) VALUES (hex(randomblob(4000)));"
	sqlite3 /srv/db/journal.db "INSERT INTO t(x) VALUES (hex(randomblob(4000)));"
	i=$((i + 1))
done
CHURN
setsid nohup sh /srv/db/churn.sh >/dev/null 2>&1 &
# Give it a moment to detach: a shell that exits at once takes its background job with it.
sleep 1
' >/dev/null
# The count waits for the writer to let go of its lock; by default the sqlite3 command gives up at once.
ROWS_BEFORE="$(sh_in "$DISTRO" 'sleep 2; sqlite3 -cmd ".timeout 10000" /srv/db/wal.db "SELECT count(*) FROM t;"')"

section "Backup while all of that is running"
run init -d "$DISTRO" --dest "$DEST" --keep 1 --yes
expect_rc 0 "init succeeds"
run run
expect_rc 0 "run succeeds with Docker and the databases busy"
expect_has "test restore passed" "and the test restore passes"
note "run took $TOOK s: $(grep -E 'wrote |changed while|test restore passed' <<<"$OUT" | sed 's/^ *//' | tr '\n' ';')"
SKIPPED_MOUNTS="$(grep -c -i 'not included\|skipped' <<<"$OUT")"
note "lines about folders that were left out: $SKIPPED_MOUNTS"
grep -i 'not included\|skipped' <<<"$OUT" | head -n 4 | sed 's/^/        | /'
ROWS_AFTER="$(sh_in "$DISTRO" 'sqlite3 -cmd ".timeout 10000" /srv/db/wal.db "SELECT count(*) FROM t;"')"
expect_true "the databases really were written to during the backup" [ "${ROWS_AFTER:-0}" -gt "${ROWS_BEFORE:-0}" ]
expect_true "the container kept running through the backup" [ "$(sh_in "$DISTRO" 'docker ps --format "{{.Names}}"' | head -n 1)" = busy ]

section "Restoring it as a new distro"
# One Docker daemon at a time: stop the one in the source before starting the copy's.
sh_in "$DISTRO" 'pkill -f churn.sh; docker stop -t 2 busy >/dev/null 2>&1; pkill dockerd; true' >/dev/null
run restore --name "$RESTORED" --to "$SANDBOX/restored" --yes
expect_rc 0 "restore succeeds"
if registered "$RESTORED"; then
	for db in wal journal; do
		CHECK="$(sh_in "$RESTORED" "sqlite3 /srv/db/$db.db 'PRAGMA integrity_check;' 2>&1 | head -n 3 | tr '\n' ' '; sqlite3 /srv/db/$db.db 'SELECT count(*) FROM t;' 2>&1 | head -n 1")"
		note "$db.db, copied while it was being written: integrity_check and row count: $CHECK"
	done
	expect_true "Docker's image store came back" [ -n "$(sh_in "$RESTORED" 'ls /var/lib/docker/overlay2 2>/dev/null | head -n 1')" ]
	expect_true "the Docker daemon starts in the restored distro" [ "$(sh_in "$RESTORED" "$DOCKERD" | tail -n 1)" = up ]
	expect_true "the image is still known" [ "$(sh_in "$RESTORED" 'docker images --format "{{.Repository}}" | head -n 1')" = busybox ]
	expect_true "and a container can be started from it" [ "$(sh_in "$RESTORED" 'docker run --rm --network none busybox echo it works 2>&1 | tail -n 1')" = "it works" ]
	note "the container that was running: $(sh_in "$RESTORED" 'docker ps -a --format "{{.Names}}: {{.Status}}" | head -n 1')"
	sh_in "$RESTORED" 'pkill dockerd; true' >/dev/null
else
	bad "the restored distro is not registered"
fi

finish
