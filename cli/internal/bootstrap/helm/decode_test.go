package helm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"testing"
)

// encodeRelease builds a Helm-3-shaped release blob: base64(gzip(json)).
func encodeRelease(t *testing.T, json string) []byte {
	t.Helper()
	var gzbuf bytes.Buffer
	gw := gzip.NewWriter(&gzbuf)
	if _, err := gw.Write([]byte(json)); err != nil {
		t.Fatal(err)
	}
	gw.Close()
	return []byte(base64.StdEncoding.EncodeToString(gzbuf.Bytes()))
}

func TestDecodeChartVersion(t *testing.T) {
	blob := encodeRelease(t, `{"name":"cert-manager","version":3,"chart":{"metadata":{"name":"cert-manager","version":"v1.14.4"}}}`)
	v, err := DecodeChartVersion(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if v != "v1.14.4" {
		t.Errorf("chart version = %q, want v1.14.4", v)
	}
}

func TestDecodeChartVersion_RawJSONFallback(t *testing.T) {
	// Not gzipped (just base64(json)) — the magic-byte check should skip
	// gunzip and parse the JSON directly.
	blob := []byte(base64.StdEncoding.EncodeToString([]byte(`{"chart":{"metadata":{"version":"1.2.3"}}}`)))
	v, err := DecodeChartVersion(blob)
	if err != nil || v != "1.2.3" {
		t.Errorf("raw-json fallback: got %q,%v want 1.2.3", v, err)
	}
}

func TestDecodeChartVersion_Errors(t *testing.T) {
	if _, err := DecodeChartVersion([]byte("!!!not base64!!!")); err == nil {
		t.Error("bad base64 must error")
	}
	// Valid base64+gzip+json but no chart version.
	blob := encodeRelease(t, `{"name":"x","chart":{"metadata":{"name":"x"}}}`)
	if _, err := DecodeChartVersion(blob); err == nil {
		t.Error("missing chart version must error")
	}
}
