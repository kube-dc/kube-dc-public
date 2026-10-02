package setup

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const sessionSchemaVersion = 1
const maxSessionBytes = 4 << 20
const maxSessionEvents = 4096

// SessionRecord is an evidence cache, never readiness or target ownership.
// References point to protected inputs; credential contents and command output
// have no fields in this schema.
type SessionRecord struct {
	SchemaVersion     int               `json:"schemaVersion"`
	ReadyToApply      bool              `json:"readyToApply"`
	ID                string            `json:"id"`
	CreatedAt         time.Time         `json:"createdAt"`
	InputHash         string            `json:"inputHash"`
	ReviewHash        string            `json:"reviewHash"`
	ReleaseSHA256     string            `json:"releaseSHA256"`
	PlanHash          string            `json:"planHash"`
	SpecFile          string            `json:"specFile"`
	ReviewFile        string            `json:"reviewFile"`
	PreparedDirectory string            `json:"preparedDirectory"`
	Targets           []SessionTarget   `json:"targets"`
	Stages            []SessionStage    `json:"stages"`
	Events            []SessionEvent    `json:"events"`
	Ownership         *SessionOwnership `json:"ownership,omitempty"`
	OwnershipEvents   []OwnershipEvent  `json:"ownershipEvents,omitempty"`
	Hash              string            `json:"hash"`
}

type SessionTarget struct {
	ID            string `json:"id"`
	MachineID     string `json:"machineId"`
	HostKeySHA256 string `json:"hostKeySHA256"`
}

type SessionStage struct {
	ID             string `json:"id"`
	Target         string `json:"target,omitempty"`
	PotentialWrite bool   `json:"potentialWrite"`
}

// An event identifies one attempt. Completion errors carry no free-form text.
// Inspection evidence is a hash of a separate protected live-state report.
type SessionEvent struct {
	Sequence       int       `json:"sequence"`
	Stage          string    `json:"stage"`
	Attempt        int       `json:"attempt"`
	State          string    `json:"state"`
	Source         string    `json:"source"`
	At             time.Time `json:"at"`
	EvidenceSHA256 string    `json:"evidenceSHA256,omitempty"`
}

type SessionSummary struct {
	ID              string         `json:"id"`
	State           string         `json:"state"`
	ReadyToApply    bool           `json:"readyToApply"`
	NeedsInspection []SessionEvent `json:"needsInspection"`
	CompletedStages int            `json:"completedStages"`
	EventCount      int            `json:"eventCount"`
	OwnershipPhase  string         `json:"ownershipPhase,omitempty"`
}

// SessionWriter holds a process lock until Close. It does not exclude another
// workstation. The coordinator must also hold verified host/cluster claims.
type SessionWriter struct {
	mu            sync.Mutex
	directory     string
	root          *os.Root
	lock          *os.File
	record        SessionRecord
	syncDirectory func(*os.Root) error
	poisoned      bool
	closed        bool
	matched       bool
}

func sessionHash(record SessionRecord) string {
	record.Hash = ""
	body, _ := json.Marshal(record)
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

func sessionStages(c Compiled) ([]SessionStage, string, error) {
	p, err := BuildPreview(c)
	if err != nil {
		return nil, "", err
	}
	stages := make([]SessionStage, 0, len(p.Stages))
	for _, stage := range p.Stages {
		stages = append(stages, SessionStage{stage.ID, stage.Target, stage.PotentialWrite})
	}
	return stages, p.PlanHash, nil
}

func sessionTargets(c Compiled, review SafetyReview) ([]SessionTarget, error) {
	targets := make([]SessionTarget, 0, len(c.Spec.Hosts))
	for _, host := range c.Spec.Hosts {
		observed, ok := reviewedHost(review.Hosts, host.ID)
		if !ok || observed.Facts.HostKey == nil || observed.Facts.MachineID == "" || observed.Facts.HostKey.FingerprintSHA256 != host.HostKeySHA256 {
			return nil, fmt.Errorf("session target has no reviewed identity")
		}
		targets = append(targets, SessionTarget{host.ID, observed.Facts.MachineID, host.HostKeySHA256})
	}
	return targets, nil
}

// CreateSession retains a validated review without acquiring remote ownership
// or running a stage. Parents must exist; an existing session is never replaced.
func CreateSession(c Compiled, review SafetyReview, directory, specFile, reviewFile string) (*SessionWriter, error) {
	return createSession(c, review, directory, specFile, reviewFile, syncSessionParent)
}

func createSession(c Compiled, review SafetyReview, directory, specFile, reviewFile string, syncParent func(string) error) (*SessionWriter, error) {
	if err := validateSafetyReview(c, review); err != nil {
		return nil, err
	}
	loaded, err := LoadSafetyReview(reviewFile)
	if err != nil || loaded.Hash != review.Hash {
		return nil, fmt.Errorf("session review reference changed")
	}
	spec, err := Load(specFile)
	if err != nil {
		return nil, err
	}
	compiled, err := Compile(spec)
	if err != nil || compiled.InputHash != c.InputHash {
		return nil, fmt.Errorf("session specification reference changed")
	}
	directory, err = privateReviewPath(directory, review.Git.Directory)
	if err != nil {
		return nil, err
	}
	stages, planHash, err := sessionStages(c)
	if err != nil {
		return nil, err
	}
	targets, err := sessionTargets(c, review)
	if err != nil {
		return nil, err
	}
	specFile, err = filepath.EvalSymlinks(specFile)
	if err != nil {
		return nil, err
	}
	specFile, err = filepath.Abs(specFile)
	if err != nil {
		return nil, err
	}
	reviewFile, err = filepath.EvalSymlinks(reviewFile)
	if err != nil {
		return nil, err
	}
	reviewFile, err = filepath.Abs(reviewFile)
	if err != nil {
		return nil, err
	}
	var id [32]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return nil, err
	}
	w, err := openSessionWriter(directory)
	if err != nil {
		return nil, err
	}
	w.record = SessionRecord{SchemaVersion: sessionSchemaVersion, ID: hex.EncodeToString(id[:]), CreatedAt: time.Now().UTC(), InputHash: c.InputHash, ReviewHash: review.Hash, ReleaseSHA256: c.ReleaseSHA256, PlanHash: planHash, SpecFile: specFile, ReviewFile: reviewFile, PreparedDirectory: review.Prepared.Directory, Targets: targets, Stages: stages, Events: []SessionEvent{}}
	if err := w.publish(w.record); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := syncParent(directory); err != nil {
		w.poisoned = true
		_ = w.Close()
		return nil, fmt.Errorf("sync new session parent failed")
	}
	w.matched = true
	return w, nil
}

func privateSessionRoot(directory string) (*os.Root, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("session directory must be a private regular directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	current, err := root.Stat(".")
	if err != nil || !os.SameFile(info, current) {
		root.Close()
		return nil, fmt.Errorf("session directory changed")
	}
	return root, nil
}

func openSessionWriter(directory string) (*SessionWriter, error) {
	root, err := privateSessionRoot(directory)
	if err != nil {
		return nil, err
	}
	before, err := root.Lstat("writer.lock")
	if err != nil && !os.IsNotExist(err) {
		root.Close()
		return nil, err
	}
	if before != nil && (!before.Mode().IsRegular() || before.Mode().Perm()&0077 != 0) {
		root.Close()
		return nil, fmt.Errorf("session lock must be a private regular file")
	}
	lock, err := root.OpenFile("writer.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		root.Close()
		return nil, err
	}
	w := &SessionWriter{directory: directory, root: root, lock: lock, syncDirectory: syncSessionDirectory}
	if err := w.checkFiles(); err != nil {
		_ = w.Close()
		return nil, err
	}
	if err := lockSessionFile(lock); err != nil {
		_ = w.Close()
		return nil, err
	}
	return w, nil
}

// OpenSession serializes with other writers. In-flight attempts from a former
// writer become uncertain before this writer can schedule anything else.
func OpenSession(directory string) (*SessionWriter, error) {
	var err error
	directory, err = filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	w, err := openSessionWriter(directory)
	if err != nil {
		return nil, err
	}
	record, err := readSessionRoot(w.root)
	if err != nil {
		_ = w.Close()
		return nil, err
	}
	w.record = record
	// A prior creation can have stopped before syncing its parent entry. Reopen
	// establishes durability again before it returns a writer for new effects.
	if err := w.syncDirectory(w.root); err != nil {
		w.poisoned = true
		_ = w.Close()
		return nil, fmt.Errorf("sync reopened session failed")
	}
	if err := syncSessionParent(directory); err != nil {
		w.poisoned = true
		_ = w.Close()
		return nil, fmt.Errorf("sync reopened session parent failed")
	}
	for _, stage := range record.Stages {
		last, ok := lastSessionEvent(w.record, stage.ID)
		if ok && last.State == "running" {
			if err := w.finish(stage.ID, last.Attempt, "uncertain", "recovery", ""); err != nil {
				_ = w.Close()
				return nil, err
			}
		}
	}
	if w.record.Ownership != nil && w.record.Ownership.Phase != "released" && w.record.Ownership.Phase != "uncertain" {
		// The previous process may have stopped during a remote operation.
		w.matched = true
		err := w.ownership(w.record.Ownership.RunID, "uncertain", w.record.Ownership.Claims, false)
		w.matched = false
		if err != nil {
			_ = w.Close()
			return nil, err
		}
	}
	return w, nil
}

// ReadSession is read-only, including when a writer is active.
func ReadSession(directory string) (SessionRecord, error) {
	root, err := privateSessionRoot(directory)
	if err != nil {
		return SessionRecord{}, err
	}
	defer root.Close()
	return readSessionRoot(root)
}

func readSessionRoot(root *os.Root) (SessionRecord, error) {
	var record SessionRecord
	info, err := root.Lstat("session.json")
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxSessionBytes {
		return record, fmt.Errorf("session snapshot must be a bounded private regular file")
	}
	f, err := root.Open("session.json")
	if err != nil {
		return record, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return record, fmt.Errorf("session snapshot changed while opening")
	}
	body, err := io.ReadAll(io.LimitReader(f, maxSessionBytes+1))
	if err != nil || len(body) > maxSessionBytes {
		return record, fmt.Errorf("session snapshot exceeds read limit")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&record); err != nil {
		return record, fmt.Errorf("invalid session JSON")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return record, fmt.Errorf("session snapshot has trailing data")
	}
	if err := validateSession(record); err != nil {
		return record, err
	}
	if err := validateStoredStageEvidence(root, record); err != nil {
		return record, err
	}
	return record, nil
}

func validSessionHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}

func lastSessionEvent(record SessionRecord, stage string) (SessionEvent, bool) {
	for i := len(record.Events) - 1; i >= 0; i-- {
		if record.Events[i].Stage == stage {
			return record.Events[i], true
		}
	}
	return SessionEvent{}, false
}

func validateSession(record SessionRecord) error {
	if record.SchemaVersion != sessionSchemaVersion || record.ReadyToApply || record.Hash != sessionHash(record) || record.CreatedAt.IsZero() || record.CreatedAt.After(time.Now().Add(time.Minute)) {
		return fmt.Errorf("session schema, hash, or state changed")
	}
	for _, hash := range []string{record.ID, record.InputHash, record.ReviewHash, record.ReleaseSHA256, record.PlanHash} {
		if !validSessionHash(hash) {
			return fmt.Errorf("session identity is incomplete")
		}
	}
	for _, path := range []string{record.SpecFile, record.ReviewFile, record.PreparedDirectory} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\r\n\x00") {
			return fmt.Errorf("session reference is invalid")
		}
	}
	stages := map[string]bool{}
	for _, stage := range record.Stages {
		if stage.ID == "" || stages[stage.ID] {
			return fmt.Errorf("session stage identity is invalid")
		}
		stages[stage.ID] = true
	}
	if len(stages) == 0 || len(record.Targets) == 0 || len(record.Events) > maxSessionEvents {
		return fmt.Errorf("session inventory is invalid")
	}
	seenTargets := map[string]bool{}
	for _, target := range record.Targets {
		if target.ID == "" || target.MachineID == "" || !ValidHostKeySHA256(target.HostKeySHA256) || seenTargets[target.ID] {
			return fmt.Errorf("session target identity is invalid")
		}
		seenTargets[target.ID] = true
	}
	last := map[string]SessionEvent{}
	previousTime := record.CreatedAt
	for i, event := range record.Events {
		old, exists := last[event.Stage]
		if event.Sequence != i+1 || !stages[event.Stage] || event.At.Before(previousTime) || event.At.After(time.Now().Add(time.Minute)) {
			return fmt.Errorf("session event order changed")
		}
		if event.State == "running" {
			if event.Source != "execution" || event.Attempt != old.Attempt+1 || (exists && old.State != "absent") || event.EvidenceSHA256 != "" {
				return fmt.Errorf("session attempt cannot start")
			}
		} else {
			if !exists || event.Attempt != old.Attempt {
				return fmt.Errorf("session completion has no matching attempt")
			}
			switch event.Source {
			case "execution", "recovery", "coordinator":
				if old.State != "running" || (event.State != "succeeded" && event.State != "uncertain") || (event.Source == "recovery" && event.State != "uncertain") || (event.Source == "coordinator" && event.State != "succeeded") {
					return fmt.Errorf("session completion state is invalid")
				}
			case "inspection":
				if old.State != "uncertain" || (event.State != "succeeded" && event.State != "absent") {
					return fmt.Errorf("session inspection state is invalid")
				}
			default:
				return fmt.Errorf("session event source is invalid")
			}
			if event.State == "uncertain" {
				if event.EvidenceSHA256 != "" {
					return fmt.Errorf("uncertain attempt has no verified evidence")
				}
			} else if !validSessionHash(event.EvidenceSHA256) {
				return fmt.Errorf("session completion needs evidence")
			}
		}
		last[event.Stage], previousTime = event, event.At
	}
	if len(record.Events)+sessionRecoveryBudget(last) > maxSessionEvents {
		return fmt.Errorf("session lacks reserved recovery capacity")
	}
	return validateOwnership(record)
}

func sessionRecoveryBudget(last map[string]SessionEvent) int {
	reserved := 0
	for _, event := range last {
		switch event.State {
		case "running":
			reserved += 2 // Uncertainty, then inspection.
		case "uncertain":
			reserved++ // Inspection.
		}
	}
	return reserved
}

// Match recomputes stage identities from sealed inputs. A snapshot or plan hash
// on its own cannot change the stages that a coordinator may journal.
func (w *SessionWriter) Match(c Compiled, review SafetyReview) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.matched = false
	if err := w.usable(); err != nil {
		return err
	}
	if err := matchSessionRecord(w.record, c, review); err != nil {
		return err
	}
	w.matched = true
	return nil
}

func (w *SessionWriter) Begin(stage string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return 0, err
	}
	if !w.matched {
		return 0, fmt.Errorf("match the session to compiled inputs and reviewed targets before a new attempt")
	}
	last, _ := lastSessionEvent(w.record, stage)
	event := SessionEvent{Sequence: len(w.record.Events) + 1, Stage: stage, Attempt: last.Attempt + 1, State: "running", Source: "execution", At: time.Now().UTC()}
	if err := w.append(event); err != nil {
		return 0, err
	}
	return event.Attempt, nil
}

// Complete is called only after the stage's postcondition is verified. Any
// failure after Begin must use uncertain, even if the effect returned an error.
func (w *SessionWriter) Complete(stage string, attempt int, evidenceSHA256 string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.finish(stage, attempt, "succeeded", "execution", evidenceSHA256)
}

func (w *SessionWriter) Uncertain(stage string, attempt int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.finish(stage, attempt, "uncertain", "execution", "")
}

// Resolve records inspection of one uncertain attempt. Absent permits an
// explicit later Begin, but never schedules a retry or grants apply readiness.
func (w *SessionWriter) Resolve(stage string, attempt int, present bool, evidenceSHA256 string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	state := "absent"
	if present {
		state = "succeeded"
	}
	return w.finish(stage, attempt, state, "inspection", evidenceSHA256)
}

func (w *SessionWriter) finish(stage string, attempt int, state, source, evidence string) error {
	if err := w.usable(); err != nil {
		return err
	}
	return w.append(SessionEvent{Sequence: len(w.record.Events) + 1, Stage: stage, Attempt: attempt, State: state, Source: source, At: time.Now().UTC(), EvidenceSHA256: evidence})
}

func (w *SessionWriter) append(event SessionEvent) error {
	next := w.record
	next.Events = append(append([]SessionEvent{}, next.Events...), event)
	next.Hash = sessionHash(next)
	if err := validateSession(next); err != nil {
		return err
	}
	return w.publish(next)
}

func (w *SessionWriter) usable() error {
	if w.closed || w.poisoned {
		return fmt.Errorf("session writer is closed or has an uncertain storage failure")
	}
	if err := w.checkFiles(); err != nil {
		w.poisoned = true
		return err
	}
	if w.record.Hash != "" {
		current, err := readSessionRoot(w.root)
		if err != nil || current.Hash != w.record.Hash {
			w.poisoned = true
			return fmt.Errorf("session snapshot changed outside this writer")
		}
	}
	return nil
}

func (w *SessionWriter) checkFiles() error {
	info, err := os.Lstat(w.directory)
	opened, openErr := w.root.Stat(".")
	if err != nil || openErr != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 || !os.SameFile(info, opened) {
		return fmt.Errorf("session directory changed")
	}
	info, err = w.root.Lstat("writer.lock")
	locked, lockErr := w.lock.Stat()
	if err != nil || lockErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !os.SameFile(info, locked) {
		return fmt.Errorf("session lock changed")
	}
	if info, err = w.root.Lstat("session.json"); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("session snapshot changed")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (w *SessionWriter) publish(next SessionRecord) error {
	if err := w.usable(); err != nil {
		return err
	}
	next.Hash = sessionHash(next)
	if err := validateSession(next); err != nil {
		return err
	}
	body, err := json.MarshalIndent(next, "", "  ")
	if err != nil || len(body)+1 > maxSessionBytes {
		return fmt.Errorf("session snapshot exceeds storage limit")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := ".session-" + hex.EncodeToString(nonce[:])
	f, err := w.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		w.poisoned = true
		return fmt.Errorf("create session snapshot failed")
	}
	defer w.root.Remove(name)
	_, writeErr := f.Write(append(body, '\n'))
	syncErr, closeErr := f.Sync(), f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		w.poisoned = true
		return fmt.Errorf("write session snapshot failed")
	}
	if err := w.checkFiles(); err != nil {
		w.poisoned = true
		return err
	}
	if err := w.root.Rename(name, "session.json"); err != nil {
		w.poisoned = true
		return fmt.Errorf("publish session snapshot failed")
	}
	if err := w.syncDirectory(w.root); err != nil {
		w.poisoned = true
		return fmt.Errorf("sync session directory failed")
	}
	w.record = next
	return nil
}

func syncSessionDirectory(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr, closeErr := dir.Sync(), dir.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func syncSessionParent(directory string) error {
	parent, err := os.OpenRoot(filepath.Dir(directory))
	if err != nil {
		return err
	}
	defer parent.Close()
	return syncSessionDirectory(parent)
}

func SummarizeSession(record SessionRecord) SessionSummary {
	result := SessionSummary{ID: record.ID, State: "prepared", NeedsInspection: []SessionEvent{}, EventCount: len(record.Events)}
	for _, stage := range record.Stages {
		last, ok := lastSessionEvent(record, stage.ID)
		if !ok {
			continue
		}
		if last.State == "succeeded" {
			result.CompletedStages++
		}
		if last.State == "running" || last.State == "uncertain" {
			result.NeedsInspection = append(result.NeedsInspection, last)
		}
	}
	if len(result.NeedsInspection) != 0 {
		result.State = "inspection-required"
	} else if result.EventCount != 0 {
		result.State = "evidence-recorded"
	}
	if record.Ownership != nil {
		result.OwnershipPhase = record.Ownership.Phase
		if record.Ownership.Phase != "released" {
			result.State = "inspection-required"
		}
	}
	return result
}

func (w *SessionWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	lockErr := w.lock.Close() // Closing releases the OS lock. Never unlink it.
	rootErr := w.root.Close()
	if lockErr != nil {
		return lockErr
	}
	return rootErr
}
