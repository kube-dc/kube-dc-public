#!/bin/sh
set -u

OUTPUT_FILE="${NFT_COUNTERS_FILE:-/run/frr/nft-counters.json}"
INTERVAL_SECONDS="${NFT_COUNTERS_INTERVAL_SECONDS:-5}"
case "$INTERVAL_SECONDS" in
  ''|*[!0-9]*) INTERVAL_SECONDS=5 ;;
  0) INTERVAL_SECONDS=1 ;;
esac

while :; do
  temporary_file="${OUTPUT_FILE}.tmp"
  rm -f "$temporary_file" >/dev/null 2>&1 || true
  if nft -j list chain inet kube_dc_routed forward >"$temporary_file"; then
    if ! chmod 0640 "$temporary_file" || ! mv -f "$temporary_file" "$OUTPUT_FILE"; then
      rm -f "$temporary_file" >/dev/null 2>&1 || true
    fi
  else
    rm -f "$temporary_file" >/dev/null 2>&1 || true
  fi

  if [ "${NFT_COUNTERS_ONESHOT:-false}" = "true" ]; then
    break
  fi
  sleep "$INTERVAL_SECONDS" || true
done
