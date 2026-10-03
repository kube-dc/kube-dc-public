package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const maxOwnershipEvents = 256

// Ownership is saved intent and observed claim evidence. It never replaces a
// fresh remote check. A run identity is never restored as a new writer's owner.
type SessionOwnership struct {
	RunID  string      `json:"runId"`
	Phase  string      `json:"phase"`
	Claims []HostClaim `json:"claims"`
}
type OwnershipEvent struct {
	RunID          string    `json:"runId"`
	Phase          string    `json:"phase"`
	At             time.Time `json:"at"`
	EvidenceSHA256 string    `json:"evidenceSHA256"`
}

func (w *SessionWriter) Snapshot() (SessionRecord, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return SessionRecord{}, err
	}
	// Return no mutable aliases into the writer.
	body, err := json.Marshal(w.record)
	if err != nil {
		return SessionRecord{}, err
	}
	var record SessionRecord
	err = json.Unmarshal(body, &record)
	return record, err
}

func (w *SessionWriter) ownership(run, phase string, claims []HostClaim, takeover bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable(); err != nil {
		return err
	}
	if !w.matched {
		return fmt.Errorf("match reviewed inputs before ownership changes")
	}
	old := w.record.Ownership
	if phase == "acquiring" {
		if old != nil && old.Phase != "released" && !(old.Phase == "uncertain" && takeover) {
			return fmt.Errorf("inspect the saved owner before another claim attempt")
		}
		if old != nil && old.RunID == run {
			return fmt.Errorf("a new ownership attempt requires a fresh run")
		}
		if len(w.record.OwnershipEvents)+4 > maxOwnershipEvents {
			return fmt.Errorf("ownership journal has no recovery capacity")
		}
	} else if old == nil || old.RunID != run || !ownershipTransition(old.Phase, phase) {
		return fmt.Errorf("ownership transition is invalid")
	}
	next := w.record
	next.Ownership = &SessionOwnership{run, phase, append([]HostClaim{}, claims...)}
	next.OwnershipEvents = append(append([]OwnershipEvent{}, next.OwnershipEvents...), OwnershipEvent{run, phase, time.Now().UTC(), fingerprintOwnership(next.Ownership)})
	return w.publish(next)
}
func fingerprintOwnership(owner *SessionOwnership) string {
	body, _ := json.Marshal(owner)
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}
func ownershipTransition(from, to string) bool {
	switch from {
	case "acquiring":
		return to == "held" || to == "uncertain"
	case "held":
		return to == "releasing" || to == "uncertain"
	case "releasing":
		return to == "released" || to == "uncertain"
	}
	return false
}
func validateOwnership(record SessionRecord) error {
	if record.Ownership == nil {
		if len(record.OwnershipEvents) != 0 {
			return fmt.Errorf("ownership log has no current owner")
		}
		return nil
	}
	o := record.Ownership
	if !validSessionHash(o.RunID) || len(o.Claims) > len(record.Targets) || len(record.OwnershipEvents) == 0 || len(record.OwnershipEvents) > maxOwnershipEvents {
		return fmt.Errorf("invalid ownership identity or limits")
	}
	if o.Phase != "acquiring" && o.Phase != "held" && o.Phase != "releasing" && o.Phase != "released" && o.Phase != "uncertain" {
		return fmt.Errorf("invalid ownership phase")
	}
	last := OwnershipEvent{}
	previous := record.CreatedAt
	for _, e := range record.OwnershipEvents {
		if !validSessionHash(e.RunID) || !validSessionHash(e.EvidenceSHA256) || e.At.Before(previous) || e.At.After(time.Now().Add(time.Minute)) {
			return fmt.Errorf("invalid ownership event")
		}
		if e.Phase == "acquiring" {
			if last.RunID == e.RunID || (last.Phase != "" && last.Phase != "released" && last.Phase != "uncertain") {
				return fmt.Errorf("invalid ownership acquisition")
			}
		} else if e.RunID != last.RunID || !ownershipTransition(last.Phase, e.Phase) {
			return fmt.Errorf("invalid ownership event transition")
		}
		last = e
		previous = e.At
	}
	if last.RunID != o.RunID || last.Phase != o.Phase || last.EvidenceSHA256 != fingerprintOwnership(o) {
		return fmt.Errorf("ownership snapshot differs from log")
	}
	reserve := 0
	if o.Phase == "acquiring" || o.Phase == "held" {
		reserve = 2
	}
	if o.Phase == "releasing" {
		reserve = 1
	}
	if len(record.OwnershipEvents)+reserve > maxOwnershipEvents {
		return fmt.Errorf("ownership lacks recovery capacity")
	}
	seen := map[string]bool{}
	for _, c := range o.Claims {
		var target SessionTarget
		for _, t := range record.Targets {
			if t.ID == c.HostID {
				target = t
			}
		}
		if seen[c.HostID] || target.ID == "" || c.MachineID != target.MachineID || (!claimBootPattern.MatchString(c.BootID) || !claimBootPattern.MatchString(c.ObservedBootID)) || c.SessionID != record.ID || c.RunID != o.RunID || c.InputHash != record.InputHash || c.ReviewHash != record.ReviewHash || c.ReleaseSHA256 != record.ReleaseSHA256 || c.ExpiresTick < 0 || c.ObservedTick < 0 || (c.State != "reserved" && c.State != "started" && c.State != "released") {
			return fmt.Errorf("invalid observed ownership claim")
		}
		seen[c.HostID] = true
	}
	if (o.Phase == "held" || o.Phase == "released") && len(o.Claims) != len(record.Targets) {
		return fmt.Errorf("ownership lacks target evidence")
	}
	for _, c := range o.Claims {
		if o.Phase == "held" && c.State != "reserved" || o.Phase == "released" && c.State != "released" {
			return fmt.Errorf("ownership evidence differs from phase")
		}
	}
	return nil
}
