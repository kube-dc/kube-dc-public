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

func TestLatestChartVersions_PicksHighestRevision(t *testing.T) {
	v2 := encodeRelease(t, `{"chart":{"metadata":{"version":"v1.0.0"}}}`)
	v3 := encodeRelease(t, `{"chart":{"metadata":{"version":"v2.0.0"}}}`)
	// Latest (rev 3) wins regardless of list order.
	for _, order := range [][]ReleaseSecret{
		{{Namespace: "x", Name: "app", Revision: 2, Data: v2}, {Namespace: "x", Name: "app", Revision: 3, Data: v3}},
		{{Namespace: "x", Name: "app", Revision: 3, Data: v3}, {Namespace: "x", Name: "app", Revision: 2, Data: v2}},
	} {
		got := LatestChartVersions(order)
		if got["x/app"] != "v2.0.0" {
			t.Errorf("latest-rev should win: got %q want v2.0.0 (order %+v)", got["x/app"], order)
		}
	}
}

// P1 regression: a corrupt LATEST revision must NOT fall back to an older
// valid revision (that would pin a downgrade on adoption) — it must be
// omitted (→ undetected), in both list orders.
func TestLatestChartVersions_CorruptLatestIsOmittedNotDowngraded(t *testing.T) {
	v2 := encodeRelease(t, `{"chart":{"metadata":{"version":"v1.0.0"}}}`)
	corrupt3 := []byte("!!!not-decodable!!!")
	for name, secrets := range map[string][]ReleaseSecret{
		"valid-then-corrupt": {{Namespace: "x", Name: "app", Revision: 2, Data: v2}, {Namespace: "x", Name: "app", Revision: 3, Data: corrupt3}},
		"corrupt-then-valid": {{Namespace: "x", Name: "app", Revision: 3, Data: corrupt3}, {Namespace: "x", Name: "app", Revision: 2, Data: v2}},
	} {
		got := LatestChartVersions(secrets)
		if _, present := got["x/app"]; present {
			t.Errorf("%s: corrupt latest must be omitted (not fall back to older rev), got %q", name, got["x/app"])
		}
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
