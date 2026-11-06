#!/usr/bin/env bash
set -euo pipefail

if [[ ${1:-} == --inside ]]; then
  [[ $(readlink /proc/self/ns/net) != $(readlink /proc/1/ns/net) ]] || { echo '拒绝修改主机网络空间' >&2; exit 1; }
  binary=$2
  network=$3
  pause=$4
  ip link set lo up
  ip link set lo mtu 1500
  tc qdisc add dev lo root handle 1: prio
  tc qdisc add dev lo parent 1:3 handle 30: netem limit 1000 delay 80ms 20ms loss 1% rate 1mbit
  tc filter add dev lo protocol ip parent 1: prio 3 u32 match ip dport 7000 0xffff flowid 1:3
  tc filter add dev lo protocol ip parent 1: prio 3 u32 match ip sport 7000 0xffff flowid 1:3
  tc qdisc show dev lo
  (
    sleep 20
    echo '开始受控链路停顿'
    tc qdisc change dev lo parent 1:3 handle 30: netem limit 1000 delay 80ms 20ms loss 100% rate 1mbit
    sleep "$pause"
    tc qdisc change dev lo parent 1:3 handle 30: netem limit 1000 delay 80ms 20ms loss 1% rate 1mbit
    echo '受控链路已恢复'
  ) &
  worker=$!
  set +e
  "$binary" -test.run "TestSlowLinkTransferAndControl/$network" -test.v -test.timeout=12m
  status=$?
  wait "$worker"
  exit "$status"
fi

mode=${1:-transfer}
network=${2:-both}
case "$mode" in
  transfer) pause=15 ;;
  recovery) pause=95; export TUNNELQUAY_SLOW_EXPECT_DROP=1 ;;
  *) echo '模式须为 transfer 或 recovery' >&2; exit 1 ;;
esac
case "$network" in
  tcp|quic) networks=$network ;;
  both) networks='tcp quic' ;;
  *) echo '传输须为 tcp、quic 或 both' >&2; exit 1 ;;
esac
for command in go unshare ip tc; do
  command -v "$command" >/dev/null || { echo "缺少测试工具：$command" >&2; exit 1; }
done
repo_dir=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_dir"
[[ -f internal/webui/dist/index.html ]] || { echo '请先执行 pnpm --dir web build' >&2; exit 1; }
run_dir=$(mktemp -d "${TMPDIR:-/tmp}/tunnelquay-slow.XXXXXXXX")
trap 'rm -f -- "$run_dir/server.test"; rmdir -- "$run_dir"' EXIT
go test -c -o "$run_dir/server.test" ./internal/server
export TUNNELQUAY_SLOW_TEST=1
export TUNNELQUAY_SLOW_BYTES=${TUNNELQUAY_SLOW_BYTES:-10485760}
for selected in $networks; do
  unshare -n bash "$repo_dir/scripts/test-slow-link.sh" --inside "$run_dir/server.test" "$selected" "$pause"
done
