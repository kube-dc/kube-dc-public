#!/bin/sh
set -eu

test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT

command_log="$test_dir/commands.log"
export COMMAND_LOG="$command_log"

mkdir -p "$test_dir/bin"
cat > "$test_dir/bin/ip" <<'EOF'
#!/bin/sh
printf 'ip %s\n' "$*" >> "$COMMAND_LOG"
if [ "$1 $2 $3" = "route del default" ]; then
  exit 1
fi
exit 0
EOF
cat > "$test_dir/bin/nft" <<'EOF'
#!/bin/sh
printf 'nft %s\n' "$*" >> "$COMMAND_LOG"
if [ "$1 $2 $3" = "list table inet" ]; then
  exit 1
fi
exit 0
EOF
cat > "$test_dir/bin/render" <<'EOF'
#!/bin/sh
cp "$1" "$2"
EOF
cat > "$test_dir/bin/exporter" <<'EOF'
#!/bin/sh
exit 0
EOF
cat > "$test_dir/bin/capsh" <<'EOF'
#!/bin/sh
printf 'capsh collector-caps=net_admin gid=101\n' >> "$COMMAND_LOG"
# A dead metrics helper must not prevent bgpd startup or gateway readiness.
exit 1
EOF
cat > "$test_dir/bin/su" <<'EOF'
#!/bin/sh
printf 'su exporter-as=frr\n' >> "$COMMAND_LOG"
exec /bin/sh -c "$5" "$6" "$7"
EOF
cat > "$test_dir/bin/bgpd" <<'EOF'
#!/bin/sh
printf 'bgpd %s\n' "$*" >> "$COMMAND_LOG"
exit 0
EOF
cat > "$test_dir/bin/zebra" <<'EOF'
#!/bin/sh
printf 'zebra %s\n' "$*" >> "$COMMAND_LOG"
exit 0
EOF
chmod 0755 "$test_dir/bin/"*

printf 'router bgp 65002\n' > "$test_dir/frr.conf"
printf 'table inet kube_dc_routed {}\n' > "$test_dir/nftables.conf"
printf '1\n' > "$test_dir/ip_forward"

PATH="$test_dir/bin:$PATH" \
PROJECT_CIDRS="10.0.30.0/24" \
ROUTING_LINK_ROUTER_IP="100.65.0.1" \
EXPECTED_PEERS="192.0.2.6,192.0.2.7" \
ALLOWED_IMPORT_PREFIXES="198.51.100.0/24" \
FRR_CONFIG_TEMPLATE="$test_dir/frr.conf" \
FRR_CONFIG_OUTPUT="$test_dir/rendered.conf" \
NFTABLES_CONFIG="$test_dir/nftables.conf" \
IP_FORWARD_PATH="$test_dir/ip_forward" \
RENDER_BIN="$test_dir/bin/render" \
EXPORTER_BIN="$test_dir/bin/exporter" \
NFT_COUNTER_COLLECTOR_BIN="$test_dir/bin/collector" \
CAPSH_BIN="$test_dir/bin/capsh" \
SU_BIN="$test_dir/bin/su" \
BGPD_BIN="$test_dir/bin/bgpd" \
ZEBRA_BIN="$test_dir/bin/zebra" \
  sh "$(dirname "$0")/entrypoint.sh"

grep -Fx 'ip route replace 10.0.30.0/24 via 100.65.0.1 dev net1' "$command_log"
grep -Fx 'zebra -f /dev/null -A 127.0.0.1 -d' "$command_log"
! grep -E '^zebra .* -S([[:space:]]|$)' "$command_log"
grep -Fx 'su exporter-as=frr' "$command_log"
grep -Fx 'capsh collector-caps=net_admin gid=101' "$command_log"
grep -Fx 'bgpd -f '"$test_dir"'/rendered.conf -A 127.0.0.1' "$command_log"
! grep -E '^bgpd .* -S([[:space:]]|$)' "$command_log"
! grep -E '^bgpd .* -(n|Z)([[:space:]]|$)' "$command_log"
! grep -F 'ip route replace 198.51.100.0/24' "$command_log"
test "$(cat "$test_dir/ip_forward")" = 1
