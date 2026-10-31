#!/bin/sh
set -eu

test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT

mkdir -p "$test_dir/bin"
cat >"$test_dir/bin/nft" <<'EOF'
#!/bin/sh
test "$*" = "-j list chain inet kube_dc_routed forward"
printf '%s\n' '{"nftables":[{"rule":{"comment":"deny transit","expr":[{"counter":{"packets":7,"bytes":448}}]}}]}'
EOF
chmod 0755 "$test_dir/bin/nft"

PATH="$test_dir/bin:$PATH" \
NFT_COUNTERS_FILE="$test_dir/counters.json" \
NFT_COUNTERS_ONESHOT=true \
  sh "$(dirname "$0")/nft-counter-collector.sh"

test -s "$test_dir/counters.json"
grep -F '"packets":7' "$test_dir/counters.json"
test "$(stat -c '%a' "$test_dir/counters.json")" = 640

cat >"$test_dir/bin/chmod" <<'EOF'
#!/bin/sh
exit 1
EOF
chmod 0755 "$test_dir/bin/chmod"
rm -f "$test_dir/counters.json"
PATH="$test_dir/bin:$PATH" \
NFT_COUNTERS_FILE="$test_dir/counters.json" \
NFT_COUNTERS_ONESHOT=true \
  sh "$(dirname "$0")/nft-counter-collector.sh"
test ! -e "$test_dir/counters.json"
test ! -e "$test_dir/counters.json.tmp"
