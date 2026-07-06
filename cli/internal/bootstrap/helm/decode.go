// Package helm decodes the Helm 3 release payload stored in a
// `helm.sh/release.v1` Secret's `release` field, so `bootstrap adopt`
// can read the LIVE chart version of a component to pin for in-place
// adoption. Pure (no k8s deps) — the k8s adapter passes the Secret bytes.
package helm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

// maxDecompressed caps the gunzip output — a Helm release blob is a few
// hundred KB at most; this guards against a decompression bomb.
const maxDecompressed = 16 * 1024 * 1024

// DecodeChartVersion extracts `.chart.metadata.version` from a Helm 3
// release Secret's `release` data field. Helm stores it as
// base64(gzip(json)); client-go already un-base64s the Secret transport
// layer, so `data` is that base64(gzip(json)) string as bytes. We
// base64-decode, gunzip (Helm always gzips; we still accept raw JSON via
// the magic-byte check for robustness), then read the chart version.
func DecodeChartVersion(data []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(data)))
	if err != nil {
		return "", fmt.Errorf("helm: base64-decode release: %w", err)
	}
	jsonBytes := raw
	if isGzip(raw) {
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return "", fmt.Errorf("helm: gunzip release: %w", err)
		}
		defer gz.Close()
		jsonBytes, err = io.ReadAll(io.LimitReader(gz, maxDecompressed))
		if err != nil {
			return "", fmt.Errorf("helm: read release: %w", err)
		}
	}
	var rel struct {
		Chart struct {
			Metadata struct {
				Version string `json:"version"`
			} `json:"metadata"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(jsonBytes, &rel); err != nil {
		return "", fmt.Errorf("helm: parse release json: %w", err)
	}
	if rel.Chart.Metadata.Version == "" {
		return "", fmt.Errorf("helm: no chart version in release payload")
	}
	return rel.Chart.Metadata.Version, nil
}

func isGzip(b []byte) bool {
	return len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b
}
