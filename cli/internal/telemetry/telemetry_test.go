package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDisabledByDefault_NoFileWritten(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KUBE_DC_TELEMETRY", "") // explicit: unset/empty = off

	Count("kube-dc bootstrap doctor")

	if _, err := os.Stat(filepath.Join(home, countersRelPath)); !os.IsNotExist(err) {
		t.Fatal("disabled telemetry must not write anything")
	}
	if Enabled() {
		t.Fatal("Enabled() must be false by default")
	}
}

func TestOnlyExactOneEnables(t *testing.T) {
	for _, v := range []string{"true", "yes", "on", "0", "2"} {
		t.Setenv("KUBE_DC_TELEMETRY", v)
		if Enabled() {
			t.Errorf("KUBE_DC_TELEMETRY=%q must NOT enable (documented value is exactly \"1\")", v)
		}
	}
	t.Setenv("KUBE_DC_TELEMETRY", "1")
	if !Enabled() {
		t.Error("KUBE_DC_TELEMETRY=1 must enable")
	}
}

func TestCount_IncrementsLocalSink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KUBE_DC_TELEMETRY", "1")

	Count("kube-dc bootstrap init")
	Count("kube-dc bootstrap init")
	Count("kube-dc bootstrap doctor")

	body, err := os.ReadFile(filepath.Join(home, countersRelPath))
	if err != nil {
		t.Fatalf("counter file not written: %v", err)
	}
	counters := map[string]int64{}
	if err := json.Unmarshal(body, &counters); err != nil {
		t.Fatalf("counter file not valid JSON: %v\n%s", err, body)
	}
	if counters["kube-dc bootstrap init"] != 2 || counters["kube-dc bootstrap doctor"] != 1 {
		t.Errorf("wrong counts: %v", counters)
	}
}

func TestCount_MalformedPriorFileStartsFresh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KUBE_DC_TELEMETRY", "1")
	path := filepath.Join(home, countersRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}

	Count("kube-dc bootstrap status") // must not panic or fail

	body, _ := os.ReadFile(path)
	counters := map[string]int64{}
	if err := json.Unmarshal(body, &counters); err != nil {
		t.Fatalf("file should be repaired to valid JSON, got: %s", body)
	}
	if counters["kube-dc bootstrap status"] != 1 {
		t.Errorf("count lost after repair: %v", counters)
	}
}

func TestCount_UnwritableHomeIsSilentNoop(t *testing.T) {
	t.Setenv("HOME", "/proc/definitely-not-writable")
	t.Setenv("KUBE_DC_TELEMETRY", "1")
	Count("kube-dc bootstrap") // must simply not panic
}
