#!/usr/bin/env bash
# exit-ip-check.sh (T-probe-exit-invariant, 2026-09-17) — инвариант владельца:
# «поиск стратегий должен идти с IP роутера (прямой путь провайдера), а если
# с IP VPS — это бессмысленно». Тот же netns-фейк-клиент, что solve.sh (veth +
# NAT как настоящий LAN-клиент), внутри — curl на IP-echo. Выводит IP выхода;
# вызывающий (invariants-ensure) сверяет: IP == VPS → пробы утекают в туннель.
# Read-only к конфигам, временные netns/veth/правила — снимаются на выходе.
set -u

NS=exitip-ns
NSIP=10.199.199.2
HOSTIP=10.199.199.1
SUBNET=10.199.199.0/30
WAN=$(ip route show default | awk '{print $5; exit}')
VPS="${1:-}"

teardown() {
  ip netns del $NS 2>/dev/null
  ip link del veth-exitip 2>/dev/null
  iptables -t nat -D POSTROUTING -s $SUBNET -o $WAN -j MASQUERADE 2>/dev/null
  iptables -t mangle -D POSTROUTING -s $NSIP -j ACCEPT 2>/dev/null
}
trap teardown EXIT
teardown 2>/dev/null

ip netns add $NS 2>/dev/null
ip link add veth-exitip type veth peer name veth-peer 2>/dev/null
ip link set veth-peer netns $NS
ip addr add $HOSTIP/30 dev veth-exitip; ip link set veth-exitip up
ip netns exec $NS ip addr add $NSIP/30 dev veth-peer
ip netns exec $NS ip link set veth-peer up
ip netns exec $NS ip link set lo up
ip netns exec $NS ip route add default via $HOSTIP
iptables -t nat -A POSTROUTING -s $SUBNET -o $WAN -j MASQUERADE
# как в solve.sh: не пускать netns-трафик на боевые mangle-правила
iptables -t mangle -I POSTROUTING 1 -s $NSIP -j ACCEPT

# DNS внутри netns — через наш резолвер (как у клиента); IP-echo — HTTPS
ip netns exec $NS sh -c "echo 'nameserver 192.168.1.132' > /tmp/exitip-resolv.conf; export RES_OPTIONS=attempts:1 timeout:4" 2>/dev/null
IP=$(ip netns exec $NS curl -sf --max-time 12 --resolve api.ipify.org:443:$(getent ahostsv4 api.ipify.org | awk '{print $1; exit}') https://api.ipify.org/ 2>/dev/null \
  || ip netns exec $NS curl -sf --max-time 12 http://ifconfig.me/ip 2>/dev/null)

if [ -z "$IP" ]; then
  echo "ERROR: netns-клиент не смог выйти в интернет (пробы не работают!)"
  exit 2
fi
echo "$IP"
[ -n "$VPS" ] && [ "$IP" = "$VPS" ] && { echo "ALARM: выход с IP VPS — пробы утекают в туннель!"; exit 1; }
exit 0
