package clusterinit

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestFinalizationRecorderRequiredOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		gpu, hami bool
		apply     func(*FinalizationRecorder)
		want      []StepID
	}{
		{
			name: "all required milestones completed",
			apply: func(r *FinalizationRecorder) {
				for _, id := range FinalizationStepIDs(false, false) {
					r.Start(id)
					r.Done(id, nil)
				}
			},
		},
		{
			name: "deferred identity and failed OpenBao cannot complete",
			apply: func(r *FinalizationRecorder) {
				r.Done(StepBreakGlass, nil)
				r.Done(StepReconcile, nil)
				r.Done(StepOpenBao, errors.New("private-token-canary"))
				r.Skip(StepKeycloakOIDC, "waiting for identity")
				r.Done(StepOIDCCutover, nil)
			},
			want: []StepID{StepOpenBao, StepKeycloakOIDC},
		},
		{
			name: "missing event is not success",
			apply: func(r *FinalizationRecorder) {
				r.Done(StepBreakGlass, nil)
				r.Done(StepReconcile, nil)
				r.Done(StepOpenBao, nil)
				r.Done(StepKeycloakOIDC, nil)
			},
			want: []StepID{StepOIDCCutover},
		},
		{
			name: "selected GPU steps remain required",
			gpu:  true,
			hami: true,
			apply: func(r *FinalizationRecorder) {
				for _, id := range FinalizationStepIDs(true, true) {
					if id == StepGPUHAMi {
						r.Skip(id, "not ready")
						continue
					}
					r.Done(id, nil)
				}
			},
			want: []StepID{StepGPUHAMi},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewFinalizationRecorder(nil, tc.gpu, tc.hami)
			tc.apply(r)
			result := r.Result()
			err := result.ActionRequired()
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatalf("completed finalization: %v", err)
				}
				return
			}
			var pending *ActionRequiredError
			if !errors.As(err, &pending) {
				t.Fatalf("expected ActionRequiredError, got %v", err)
			}
			if !reflect.DeepEqual(pending.Steps, tc.want) || pending.ExitCode() != 2 {
				t.Fatalf("got steps %v and exit %d, want steps %v and exit 2", pending.Steps, pending.ExitCode(), tc.want)
			}
			if strings.Contains(err.Error(), "private-token-canary") {
				t.Fatal("raw adapter error leaked into the result")
			}
		})
	}
}

func TestManagedServicesIsRequiredOnlyWhenSelected(t *testing.T) {
	steps := FinalizationStepIDs(false, false, true)
	if steps[len(steps)-1] != StepManagedServices {
		t.Fatalf("managed-services finalization is not last: %v", steps)
	}
	recorder := NewFinalizationRecorder(nil, false, false, true)
	for _, id := range FinalizationStepIDs(false, false) {
		recorder.Done(id, nil)
	}
	if unmet := recorder.Result().Unmet(); len(unmet) != 1 || unmet[0].ID != StepManagedServices {
		t.Fatalf("unpublished services reported complete: %v", unmet)
	}
}
