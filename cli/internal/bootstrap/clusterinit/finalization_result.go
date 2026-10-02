package clusterinit

import (
	"fmt"
	"strings"
	"sync"
)

// FinalizationStatus describes one required post-apply milestone.
type FinalizationStatus string

const (
	FinalizationPending FinalizationStatus = "pending"
	FinalizationDone    FinalizationStatus = "done"
	FinalizationFailed  FinalizationStatus = "failed"
	FinalizationSkipped FinalizationStatus = "skipped"
)

type FinalizationStep struct {
	ID     StepID
	Status FinalizationStatus
	Reason string
}

// FinalizationResult records the outcomes that must be satisfied before init
// can report completion. A missing event is pending, not an implicit success.
type FinalizationResult struct {
	Steps []FinalizationStep
}

func (r FinalizationResult) Unmet() []FinalizationStep {
	var unmet []FinalizationStep
	for _, step := range r.Steps {
		if step.Status != FinalizationDone {
			unmet = append(unmet, step)
		}
	}
	return unmet
}

// ActionRequiredError contains stable step IDs only. The detailed re-run hints
// are already in the transcript; a raw adapter error may contain credentials.
type ActionRequiredError struct {
	Steps []StepID
}

func (e *ActionRequiredError) Error() string {
	ids := make([]string, len(e.Steps))
	for i, id := range e.Steps {
		ids[i] = string(id)
	}
	return fmt.Sprintf("installation action required for %s; review the finalization hints and rerun after resolving them", strings.Join(ids, ", "))
}

func (e *ActionRequiredError) ExitCode() int { return 2 }

func (r FinalizationResult) ActionRequired() error {
	unmet := r.Unmet()
	if len(unmet) == 0 {
		return nil
	}
	ids := make([]StepID, len(unmet))
	for i, step := range unmet {
		ids[i] = step.ID
	}
	return &ActionRequiredError{Steps: ids}
}

// FinalizationStepIDs is ordered like the finalization part of InstallSteps.
// Controller authentication is part of the OpenBao milestone in the current
// engine, so a separate StepControllerAuth is not expected here.
func FinalizationStepIDs(gpuEnabled, hamiEnabled bool, managedServices ...bool) []StepID {
	ids := []StepID{StepBreakGlass, StepReconcile}
	if gpuEnabled {
		ids = append(ids, GPUInstallStepIDs(hamiEnabled)...)
	}
	ids = append(ids, StepOpenBao, StepKeycloakOIDC, StepOIDCCutover)
	if len(managedServices) > 0 && managedServices[0] {
		ids = append(ids, StepManagedServices)
	}
	return ids
}

// FinalizationRecorder forwards events to the existing UI or plain reporter
// while retaining outcomes for a final correctness decision.
type FinalizationRecorder struct {
	mu       sync.Mutex
	next     StepReporter
	expected []StepID
	states   map[StepID]FinalizationStep
}

func NewFinalizationRecorder(next StepReporter, gpuEnabled, hamiEnabled bool, managedServices ...bool) *FinalizationRecorder {
	return &FinalizationRecorder{
		next:     reporterOrNop(next),
		expected: FinalizationStepIDs(gpuEnabled, hamiEnabled, managedServices...),
		states:   make(map[StepID]FinalizationStep),
	}
}

func (r *FinalizationRecorder) Plan(steps []Step) { r.next.Plan(steps) }

func (r *FinalizationRecorder) Start(id StepID) { r.next.Start(id) }

func (r *FinalizationRecorder) Done(id StepID, err error) {
	status := FinalizationDone
	if err != nil {
		status = FinalizationFailed
	}
	r.mu.Lock()
	r.states[id] = FinalizationStep{ID: id, Status: status}
	r.mu.Unlock()
	r.next.Done(id, err)
}

func (r *FinalizationRecorder) Skip(id StepID, reason string) {
	r.mu.Lock()
	r.states[id] = FinalizationStep{ID: id, Status: FinalizationSkipped, Reason: reason}
	r.mu.Unlock()
	r.next.Skip(id, reason)
}

func (r *FinalizationRecorder) Result() FinalizationResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := FinalizationResult{Steps: make([]FinalizationStep, 0, len(r.expected))}
	for _, id := range r.expected {
		step, ok := r.states[id]
		if !ok {
			step = FinalizationStep{ID: id, Status: FinalizationPending}
		}
		result.Steps = append(result.Steps, step)
	}
	return result
}
