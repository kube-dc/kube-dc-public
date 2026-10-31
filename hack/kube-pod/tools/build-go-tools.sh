#!/usr/bin/env bash
# Rebuild cloud-shell clients with the committed security dependency locks.
set -euo pipefail
HERE=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
OUT=${1:?usage: build-go-tools.sh OUTPUT_DIRECTORY}
GO=${GO:-go}
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
[[ $("$GO" env GOVERSION) == go1.27.3 ]] || {
  echo 'Cloud-shell client builds require Go 1.27.3' >&2
  exit 1
}
[[ $("$GO" env GOHOSTARCH) == amd64 && $("$GO" env GOHOSTOS) == linux ]] || {
  echo 'This lock qualifies linux/amd64 clients only' >&2
  exit 1
}
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
export CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 GOTOOLCHAIN=local GOWORK=off

for tool in stern k9s; do
  if [[ "$tool" == stern ]]; then
    module=github.com/stern/stern; version=v1.35.0
    sum='h1:DnZrpdzOj83bKUcSgKsJ6jVsjieWoThDD+Cti/4GD+I='
    commit=37fcd4cfc29f61dcd1cdfdb68de8a75e8b33c5fe
    built_version=1.35.0+kube-dc.20261030.1
  else
    module=github.com/derailed/k9s; version=v0.51.0
    sum='h1:do0fkB1JuN9uURg6bH99I/6phINeFmaGJcldWn8GwSY='
    commit=558caafe7ba067467de46b320cc22ef11fef9c34
    built_version=v0.51.0+kube-dc.20261030.1
  fi
  "$GO" mod download -json "$module@$version" > "$OUT/$tool-source.json"
  source_dir=$(python3 - "$OUT/$tool-source.json" "$sum" "$commit" <<'PY'
import json, sys
record = json.load(open(sys.argv[1]))
assert record['Sum'] == sys.argv[2], 'Upstream source checksum changed'
assert record['Origin']['Hash'] == sys.argv[3], 'Upstream source revision changed'
print(record['Dir'])
PY
)
  cp -R "$source_dir" "$WORK/$tool"
  chmod -R u+w "$WORK/$tool"
  cp "$HERE/$tool/go.mod" "$HERE/$tool/go.sum" "$WORK/$tool/"
  (
    cd "$WORK/$tool"
    "$GO" build -mod=readonly -trimpath -buildvcs=false \
      -ldflags="-s -w -X $module/cmd.version=$built_version -X $module/cmd.commit=$commit" \
      -o "$OUT/$tool" .
    cmp go.mod "$HERE/$tool/go.mod"
    cmp go.sum "$HERE/$tool/go.sum"
  )
done
(
  cd "$HERE/kubectl"
  "$GO" build -mod=readonly -trimpath -buildvcs=false \
    -ldflags='-s -w -X k8s.io/component-base/version.gitVersion=v1.35.9-kube-dc.1 -X k8s.io/component-base/version.gitTreeState=dirty -X k8s.io/component-base/version.gitMajor=1 -X k8s.io/component-base/version.gitMinor=35' \
    -o "$OUT/kubectl" .
)
for tool in stern k9s kubectl; do
  "$GO" version -m "$OUT/$tool" > "$OUT/$tool-buildinfo.txt"
done
(cd "$OUT" && sha256sum stern k9s kubectl > go-tools.sha256)
