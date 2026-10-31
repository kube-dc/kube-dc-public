#!/bin/sh
set -eu

PROJECT_INTERFACE="${PROJECT_INTERFACE:-net1}"
TRANSIT_INTERFACE="${TRANSIT_INTERFACE:-net2}"
FRR_CONFIG_TEMPLATE="${FRR_CONFIG_TEMPLATE:-/etc/kube-dc/frr/frr.conf}"
NFTABLES_CONFIG="${NFTABLES_CONFIG:-/etc/kube-dc/nftables/nftables.conf}"
BGP_PASSWORD_FILE="${BGP_PASSWORD_FILE:-/etc/kube-dc/bgp/password}"
FRR_CONFIG_OUTPUT="${FRR_CONFIG_OUTPUT:-/run/frr/frr.conf}"
IP_FORWARD_PATH="${IP_FORWARD_PATH:-/proc/sys/net/ipv4/ip_forward}"
RENDER_BIN="${RENDER_BIN:-/usr/local/bin/kube-dc-render-frr-config}"
EXPORTER_BIN="${EXPORTER_BIN:-/usr/local/bin/kube-dc-routing-exporter}"
NFT_COUNTER_COLLECTOR_BIN="${NFT_COUNTER_COLLECTOR_BIN:-/usr/local/bin/kube-dc-nft-counter-collector}"
CAPSH_BIN="${CAPSH_BIN:-/usr/sbin/capsh}"
SU_BIN="${SU_BIN:-/bin/su}"
BGPD_BIN="${BGPD_BIN:-/usr/lib/frr/bgpd}"
ZEBRA_BIN="${ZEBRA_BIN:-/usr/lib/frr/zebra}"

require_value() {
  value="$(eval "printf '%s' \"\${$1:-}\"")"
  if [ -z "$value" ]; then
    echo "required environment variable $1 is empty" >&2
    exit 1
  fi
}

require_value PROJECT_CIDRS
require_value ROUTING_LINK_ROUTER_IP
require_value EXPECTED_PEERS

if [ ! -r "$FRR_CONFIG_TEMPLATE" ]; then
  echo "FRR config template is not readable" >&2
  exit 1
fi
if [ ! -r "$NFTABLES_CONFIG" ]; then
  echo "nftables config is not readable" >&2
  exit 1
fi

for interface in "$PROJECT_INTERFACE" "$TRANSIT_INTERFACE"; do
  attempts=0
  until ip link show dev "$interface" >/dev/null 2>&1; do
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 60 ]; then
      echo "network interface $interface did not appear" >&2
      exit 1
    fi
    sleep 1
  done
done

# The infra eth0 default remains available for platform Prometheus and health
# traffic. Neither data-plane interface may have a default; nftables also has a
# default-drop forward policy, so an imported destination can never escape via
# the infra network when BGP withdraws it.
for interface in "$PROJECT_INTERFACE" "$TRANSIT_INTERFACE"; do
  while ip route del default dev "$interface" >/dev/null 2>&1; do :; done
done

old_ifs="$IFS"
IFS=','
for cidr in $PROJECT_CIDRS; do
  if [ -n "$cidr" ]; then
    ip route replace "$cidr" via "$ROUTING_LINK_ROUTER_IP" dev "$PROJECT_INTERFACE"
  fi
done
IFS="$old_ifs"

if [ "$(cat "$IP_FORWARD_PATH")" != 1 ]; then
  echo "IPv4 forwarding is disabled; set net.ipv4.ip_forward=1 in the pod sandbox" >&2
  exit 1
fi

# The generated ruleset starts with `flush table`; create the empty table on
# first boot so the same atomic load works both initially and on restart.
nft list table inet kube_dc_routed >/dev/null 2>&1 || nft add table inet kube_dc_routed
nft -f "$NFTABLES_CONFIG"

"$RENDER_BIN" \
  "$FRR_CONFIG_TEMPLATE" "$FRR_CONFIG_OUTPUT" "$BGP_PASSWORD_FILE"
# The directory is setgid root:frr in the image, so this root-created file
# inherits group frr. chmod is permitted to the owner without CAP_CHOWN or
# CAP_DAC_OVERRIDE and lets the privilege-dropped FRR daemons read it.
chmod 0640 "$FRR_CONFIG_OUTPUT"

# Counter collection needs netlink CAP_NET_ADMIN, while the HTTP exporter must
# remain unprivileged. Keep the helper in this container so a metrics-only
# process can never influence the Pod Ready condition used for BGP steering.
# capsh narrows the helper's permitted/effective set to NET_ADMIN; no-new-privs
# prevents it from reacquiring the gateway process's other bounded caps.
"$CAPSH_BIN" --gid=101 --caps=cap_net_admin+ep --noamb --shell=/bin/sh -- \
  -c 'exec "$1"' _ "$NFT_COUNTER_COLLECTOR_BIN" &

# Zebra makes BGP load-bearing: only routes accepted by the generated import
# policy enter the kernel FIB, and a peer withdrawal removes them. The pinned
# FRR image performs its normal root-to-frr privilege drop; SETUID and SETGID
# are present solely for that transition, while the daemon retains only the
# routing capabilities it needs.
"$ZEBRA_BIN" -f /dev/null -A 127.0.0.1 -d
"$SU_BIN" -s /bin/sh frr -c 'exec "$1"' _ "$EXPORTER_BIN" &

# Keep bgpd in the foreground as PID 1 after its own privilege drop. Unlike
# zebra, bgpd supports -S, but using it would unnecessarily keep bgpd as root.
exec "$BGPD_BIN" -f "$FRR_CONFIG_OUTPUT" -A 127.0.0.1
