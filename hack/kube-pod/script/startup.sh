#!/bin/bash

set -o errexit
set -o nounset
set -o pipefail

ONCE=${ONCE:-}
URLARG=${URLARG:-}
COMMAND=${COMMAND:-"bash"}
PS1=${PS1:-}
SERVER_BUFFER_SIZE=${SERVER_BUFFER_SIZE:-}
PING_INTERVAL=${PING_INTERVAL:-}
CLIENT_OPTIONS=${CLIENT_OPTIONS:-}
TTL=${TTL:-}

if [ -d /root -a "`ls /root`" != "" ]; then         
  rm -rf /root/*                                    
fi

source /root/.bashrc

once=""
index=""
urlarg=""
server_buffer_size=""
ping_interval=""
client_options=()

if [[ "${ONCE}" == "true" ]];then
  once=" --once "
fi

if [[ -f /usr/lib/ttyd/index.html ]]; then
  index=" --index /usr/lib/ttyd/index.html "
fi

if [[ "${URLARG}" == "true" ]];then
  urlarg=" -a "
fi

if [[ -n "${SERVER_BUFFER_SIZE}" ]]; then
  server_buffer_size=" --serv_buffer_size ${SERVER_BUFFER_SIZE} "
fi

if [[ -n "${PING_INTERVAL}" ]]; then
  ping_interval=" --ping-interval ${PING_INTERVAL} "
fi

if [[ -n "${CLIENT_OPTIONS}" ]]; then
  IFS='|' read -ra OPTIONS <<< "${CLIENT_OPTIONS}"
  for option in "${OPTIONS[@]}"; do
    client_options+=("--client-option" "${option}")
  done
fi

# Set default value for TTL if not defined
: "${TTL:=0}"

TTLCMD=""

if [[ -z "${TTL}" ]] || [[ "${TTL}" == "0" ]]; then
  TTLCMD=""
else
  TTLCMD="timeout ${TTL}"
fi

# Using exec to replace the current shell process with ttyd
# This ensures that signals sent to the container are properly forwarded to ttyd
echo "Starting ttyd with exec to properly handle signals"
${TTLCMD} ttyd -W ${index} ${once} ${urlarg} ${server_buffer_size} ${ping_interval} "${client_options[@]}" sh -c "${COMMAND}" || true
