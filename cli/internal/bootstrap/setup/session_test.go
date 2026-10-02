package setup

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func sessionFixture(t *testing.T) (*SessionWriter, Compiled, SafetyReview) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("session process tests require a Unix workstation")
	}
	c, review, _, _ := safetyReviewFixture(t)
	parent := t.TempDir()
	reviewFile := filepath.Join(parent, "review.json")
	if err := SaveSafetyReview(reviewFile, review); err != nil {
		t.Fatal(err)
	}
	specFile := filepath.Join(parent, "setup.json")
	body, err := json.Marshal(c.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specFile, body, 0600); err != nil {
		t.Fatal(err)
	}
	w, err := CreateSession(c, review, filepath.Join(parent, "session"), specFile, reviewFile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, c, review
}

func TestSessionRecordsPrivateBoundEvidence(t *testing.T) {
	w, c, review := sessionFixture(t)
	if err := w.Match(c, review); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{w.directory, filepath.Join(w.directory, "writer.lock"), filepath.Join(w.directory, "session.json")} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("public session entry: %s", path)
		}
	}
	record, err := ReadSession(w.directory)
	if err != nil {
		t.Fatal(err)
	}
	summary := SummarizeSession(record)
	if record.ReadyToApply || summary.ReadyToApply || summary.State != "prepared" || len(record.Events) != 0 || record.ReviewHash != review.Hash {
		t.Fatal("session invented execution or lost binding")
	}
	body, _ := os.ReadFile(filepath.Join(w.directory, "session.json"))
	for _, forbidden := range []string{"scriptBody", "stringData", "client-key-data", "PackageChanges"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatal("session copied effects or credential contents")
		}
	}
}

func TestSessionKilledWriterNeedsInspectionBeforeRetry(t *testing.T) {
	w, c, review := sessionFixture(t)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSessionProcessHelper$")
	cmd.Env = append(os.Environ(), "KUBE_DC_TEST_SESSION="+w.directory, "KUBE_DC_TEST_SESSION_ACTION=crash")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "intent saved" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("child did not save intent: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	reopened, err := OpenSession(w.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.Match(c, review); err != nil {
		t.Fatal(err)
	}
	record, err := ReadSession(w.directory)
	if err != nil {
		t.Fatal(err)
	}
	summary := SummarizeSession(record)
	if summary.State != "inspection-required" || len(summary.NeedsInspection) != 1 || summary.NeedsInspection[0].State != "uncertain" {
		t.Fatal("crash was treated as no effect")
	}
	if _, err := reopened.Begin("rke2-first-server"); err == nil {
		t.Fatal("uncertain stage retried")
	}
	if err := reopened.Resolve("rke2-first-server", 2, false, strings.Repeat("a", 64)); err == nil {
		t.Fatal("inspection resolved another attempt")
	}
	if err := reopened.Resolve("rke2-first-server", 1, false, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if attempt, err := reopened.Begin("rke2-first-server"); err != nil || attempt != 2 {
		t.Fatalf("explicit inspected retry failed: %v", err)
	}
}

func TestSessionProcessHelper(t *testing.T) {
	directory := os.Getenv("KUBE_DC_TEST_SESSION")
	if directory == "" {
		return
	}
	w, err := OpenSession(directory)
	if os.Getenv("KUBE_DC_TEST_SESSION_ACTION") == "locked" {
		if err == nil {
			_ = w.Close()
			os.Exit(2)
		}
		if !strings.Contains(err.Error(), "lock is unavailable") {
			os.Exit(3)
		}
		return
	}
	if err != nil {
		os.Exit(4)
	}
	spec, err := Load(w.record.SpecFile)
	if err != nil {
		os.Exit(6)
	}
	c, err := Compile(spec)
	if err != nil {
		os.Exit(7)
	}
	review, err := LoadSafetyReview(w.record.ReviewFile)
	if err != nil || w.Match(c, review) != nil {
		os.Exit(8)
	}
	if _, err := w.Begin("rke2-first-server"); err != nil {
		os.Exit(5)
	}
	fmt.Println("intent saved")
	for {
		time.Sleep(time.Hour)
	}
}

func TestSessionExcludesAnotherProcessWithoutUnlinkingLock(t *testing.T) {
	w, _, _ := sessionFixture(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionProcessHelper$")
	cmd.Env = append(os.Environ(), "KUBE_DC_TEST_SESSION="+w.directory, "KUBE_DC_TEST_SESSION_ACTION=locked")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second process was not blocked: %v %s", err, out)
	}
	before, err := os.Stat(filepath.Join(w.directory, "writer.lock"))
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	reopened, err := OpenSession(w.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := os.Stat(filepath.Join(w.directory, "writer.lock"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("writer lock inode was replaced")
	}
}

func TestSessionConcurrentTransitionsAndVerifiedCompletion(t *testing.T) {
	w, _, _ := sessionFixture(t)
	var wg sync.WaitGroup
	for _, stage := range w.record.Stages[:6] {
		wg.Add(1)
		go func(stage string) {
			defer wg.Done()
			attempt, err := w.Begin(stage)
			if err != nil {
				t.Error(err)
				return
			}
			if err := w.Complete(stage, attempt, strings.Repeat("b", 64)); err != nil {
				t.Error(err)
			}
		}(stage.ID)
	}
	wg.Wait()
	record, err := ReadSession(w.directory)
	if err != nil {
		t.Fatal(err)
	}
	if summary := SummarizeSession(record); summary.CompletedStages != 6 || summary.EventCount != 12 || summary.ReadyToApply {
		t.Fatal("concurrent transitions lost evidence or granted readiness")
	}
	if _, err := w.Begin(record.Stages[0].ID); err == nil {
		t.Fatal("verified stage automatically repeated")
	}
}

func TestSessionPublicationFailurePoisonsWriterAndRecoversIntent(t *testing.T) {
	w, _, _ := sessionFixture(t)
	w.syncDirectory = func(*os.Root) error { return errors.New("synthetic storage failure after rename") }
	if _, err := w.Begin("rke2-first-server"); err == nil {
		t.Fatal("failed durability returned permission to execute")
	}
	if _, err := w.Begin("rke2-first-server"); err == nil {
		t.Fatal("uncertain publication reused a writer")
	}
	_ = w.Close()
	reopened, err := OpenSession(w.directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	record, err := ReadSession(w.directory)
	if err != nil {
		t.Fatal(err)
	}
	if summary := SummarizeSession(record); len(summary.NeedsInspection) != 1 || summary.NeedsInspection[0].State != "uncertain" {
		t.Fatal("published intent was lost after sync failure")
	}
}

func TestSessionRefusesReplacedLockAndEditedSnapshot(t *testing.T) {
	for _, variant := range []string{"lock", "snapshot"} {
		t.Run(variant, func(t *testing.T) {
			w, _, _ := sessionFixture(t)
			if variant == "lock" {
				if err := os.Rename(filepath.Join(w.directory, "writer.lock"), filepath.Join(w.directory, "old.lock")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(w.directory, "writer.lock"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(w.directory, "session.json"), []byte(`{"changed":true}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Begin("rke2-first-server"); err == nil {
				t.Fatal("changed storage accepted")
			}
			if !w.poisoned {
				t.Fatal("changed writer was not fenced")
			}
		})
	}
}

func TestSessionRejectsUnsafeFilesCorruptionAndWrongStagePlan(t *testing.T) {
	for _, variant := range []string{"unknown", "hash", "trailing", "public", "symlink", "oversize", "stage-plan", "event-sequence"} {
		t.Run(variant, func(t *testing.T) {
			w, c, review := sessionFixture(t)
			_ = w.Close()
			path := filepath.Join(w.directory, "session.json")
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "unknown":
				body = append([]byte(`{"unknown":true,`), body[1:]...)
			case "hash":
				body = []byte(strings.Replace(string(body), `"readyToApply": false`, `"readyToApply": true`, 1))
			case "trailing":
				body = append(body, []byte("\n{}")...)
			case "public":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("session.json.original", path); err != nil {
					t.Fatal(err)
				}
			case "oversize":
				body = make([]byte, maxSessionBytes+1)
			case "stage-plan", "event-sequence":
				var record SessionRecord
				if err := json.Unmarshal(body, &record); err != nil {
					t.Fatal(err)
				}
				if variant == "stage-plan" {
					record.Stages[0].ID = "forged-stage"
				} else {
					record.Events = []SessionEvent{{Sequence: 2, Stage: record.Stages[0].ID, Attempt: 1, State: "running", Source: "execution", At: time.Now().UTC()}}
				}
				record.Hash = sessionHash(record)
				body, _ = json.Marshal(record)
			}
			if variant != "public" && variant != "symlink" {
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if variant == "stage-plan" {
				reopened, err := OpenSession(w.directory)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				if err := reopened.Match(c, review); err == nil {
					t.Fatal("forged stages matched compiled plan")
				}
			} else if _, err := ReadSession(w.directory); err == nil {
				t.Fatal("unsafe session was accepted")
			}
		})
	}
}

func TestSessionParentSyncFailureCannotReturnAnExecutionWriter(t *testing.T) {
	w, c, review := sessionFixture(t)
	directory := filepath.Join(t.TempDir(), "failed-session")
	returned, err := createSession(c, review, directory, w.record.SpecFile, w.record.ReviewFile, func(string) error { return errors.New("synthetic parent sync failure") })
	if err == nil || returned != nil {
		t.Fatal("undurable session returned a writer")
	}
	record, err := ReadSession(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Events) != 0 || record.ReadyToApply {
		t.Fatal("failed creation invented effects")
	}
	// A fresh open does not inherit creation's compiled-input match.
	reopened, err := OpenSession(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.Begin("rke2-first-server"); err == nil {
		t.Fatal("reopen bypassed input matching")
	}
}

func TestSessionReservesRecoveryAndInspectionAtEventLimit(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprintf("concurrent-%v", concurrent), func(t *testing.T) {
			w, c, review := sessionFixture(t)
			next := w.record
			// Seed valid historical attempts without thousands of disk writes.
			stage := next.Stages[0].ID
			for attempt := 1; attempt <= 1363; attempt++ {
				for _, state := range []string{"running", "uncertain", "absent"} {
					source, evidence := "execution", ""
					if state == "absent" {
						source, evidence = "inspection", strings.Repeat("a", 64)
					}
					next.Events = append(next.Events, SessionEvent{Sequence: len(next.Events) + 1, Stage: stage, Attempt: attempt, State: state, Source: source, At: next.CreatedAt, EvidenceSHA256: evidence})
				}
			}
			if err := w.publish(next); err != nil {
				t.Fatal(err)
			}
			if !concurrent {
				for _, stage := range w.record.Stages[1:3] {
					attempt, err := w.Begin(stage.ID)
					if err != nil {
						t.Fatal(err)
					}
					if err := w.Complete(stage.ID, attempt, strings.Repeat("b", 64)); err != nil {
						t.Fatal(err)
					}
				}
			}
			pending := []string{w.record.Stages[3].ID}
			if concurrent {
				pending = append(pending, w.record.Stages[4].ID)
			}
			for _, stage := range pending {
				if _, err := w.Begin(stage); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := w.Begin(w.record.Stages[5].ID); err == nil {
				t.Fatal("new intent consumed reserved recovery slots")
			}
			_ = w.Close()
			reopened, err := OpenSession(w.directory)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if err := reopened.Match(c, review); err != nil {
				t.Fatal(err)
			}
			for _, stage := range pending {
				if err := reopened.Resolve(stage, 1, false, strings.Repeat("c", 64)); err != nil {
					t.Fatal(err)
				}
			}
			record, err := ReadSession(w.directory)
			if err != nil {
				t.Fatal(err)
			}
			if len(SummarizeSession(record).NeedsInspection) != 0 {
				t.Fatal("capacity prevented inspection")
			}
			if !concurrent && len(record.Events) != maxSessionEvents {
				t.Fatal("test did not reach the exact event limit")
			}
			if _, err := reopened.Begin(pending[0]); err == nil {
				t.Fatal("full journal started another effect")
			}
		})
	}
}
