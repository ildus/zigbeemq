#!/usr/bin/env bash
# Cross-compile zigbeemq for OpenWrt aarch64 and install over SSH.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HOST="${SSH_HOST:-router}"
GO="${GO:-go}"

PORT="${ZIGBEEMQ_PORT:-/dev/ttyUSB0}"
LISTEN="${ZIGBEEMQ_LISTEN:-0.0.0.0:8080}"
HTTP_PORT="${LISTEN##*:}"
DATA="${ZIGBEEMQ_DATA:-/etc/zigbeemq}"
MQTT="${ZIGBEEMQ_MQTT:-tcp://bb:1883}"
MQTT_BASE="${ZIGBEEMQ_MQTT_BASE:-zigbee2mqtt}"

OUT="$ROOT/dist/zigbeemq-linux-arm64"
INIT_SRC="$ROOT/openwrt/zigbeemq.init"
CFG_SRC="$ROOT/openwrt/zigbeemq.config"

# OpenWrt dropbear has no sftp-server; OpenSSH 9+ scp defaults to SFTP.
scp_to() {
	scp -O -q "$1" "$HOST:$2"
}

export PATH="${HOME}/.local/go/bin:${PATH}"
export GOMODCACHE="${GOMODCACHE:-${HOME}/.local/gopath/pkg/mod}"
export GOPATH="${GOPATH:-${HOME}/.local/gopath}"
export CGO_ENABLED=0
export GOOS=linux
export GOARCH=arm64

cd "$ROOT"
mkdir -p "$ROOT/dist"

echo ">> build linux/arm64 (static)"
"$GO" build -trimpath -ldflags '-s -w' -o "$OUT" ./cmd/zigbeemq
file "$OUT"

echo ">> ssh $HOST"
ssh -o BatchMode=yes "$HOST" 'uname -m; command -v apk >/dev/null && apk add kmod-usb-serial-cp210x >/dev/null || true'

echo ">> stop service"
ssh "$HOST" "/etc/init.d/zigbeemq stop >/dev/null 2>&1 || true; killall zigbeemq >/dev/null 2>&1 || true; sleep 2"

echo ">> install binary + init"
scp_to "$OUT" /usr/sbin/zigbeemq
scp_to "$INIT_SRC" /etc/init.d/zigbeemq
ssh "$HOST" "chmod 755 /usr/sbin/zigbeemq /etc/init.d/zigbeemq; mkdir -p '$DATA'"

if ssh "$HOST" "test -f /etc/config/zigbeemq"; then
	echo ">> keep existing /etc/config/zigbeemq"
else
	echo ">> install default /etc/config/zigbeemq"
	scp_to "$CFG_SRC" /etc/config/zigbeemq
fi

LOCAL_CACHE="$ROOT/data/devices.json"
if [ -f "$LOCAL_CACHE" ]; then
	if ssh "$HOST" "test -f '$DATA/devices.json'"; then
		echo ">> keep existing $DATA/devices.json"
	else
		echo ">> copy data/devices.json"
		scp_to "$LOCAL_CACHE" "$DATA/devices.json"
	fi
fi

echo ">> enable + start"
ssh "$HOST" "/etc/init.d/zigbeemq enable; /etc/init.d/zigbeemq start"

echo ">> wait for http"
ok=0
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
	if out=$(ssh "$HOST" "wget -q -O- http://127.0.0.1:${HTTP_PORT}/api/network" 2>/dev/null) && [ -n "$out" ]; then
		printf '%s\n' "$out"
		ok=1
		break
	fi
	sleep 1
done
if [ "$ok" != 1 ]; then
	echo "!! http did not come up, last logs:" >&2
	ssh "$HOST" "logread -e zigbeemq | tail -30" >&2
	exit 1
fi

echo
echo "UI: http://<router-lan>:${HTTP_PORT}/  (SSH host: $HOST)"
echo "listen=$LISTEN port=$PORT mqtt=$MQTT data=$DATA"
