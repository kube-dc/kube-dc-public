package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestWriteGPUInstallCompletionNeverClaimsAutomaticEntitlement(t *testing.T) {
	var out bytes.Buffer
	writeGPUInstallCompletion(&out)
	got := out.String()
	for _, want := range []string{"GPU platform installation is ready", "granted no billable tenant GPU quota", "GPU add-on", "bootstrap doctor", "Accelerators"} {
		if !strings.Contains(got, want) {
			t.Fatalf("completion missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[gpu]") {
		t.Fatalf("completion invented forbidden GPU log prefix:\n%s", got)
	}
}

// fakeGRF adapts a func to the minimal interface waitPodRunning needs.
type fakeGRF func(ctx context.Context, group, version, resource, namespace, name string, fields ...string) (string, error)

func (f fakeGRF) GetResourceFieldFirst(ctx context.Context, group, version, resource, namespace, name string, fields ...string) (string, error) {
	return f(ctx, group, version, resource, namespace, name, fields...)
}

func TestWaitPodRunning_ReturnsWhenRunning(t *testing.T) {
	// Speed up: timeAfter fires immediately so the poll loop doesn't
	// sleep in real time.
	origAfter := timeAfter
	timeAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	defer func() { timeAfter = origAfter }()

	calls := 0
	fake := fakeGRF(func(_ context.Context, _, _, _, _, _ string, _ ...string) (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("pods \"openbao-0\" not found")
		}
		return "Running", nil
	})

	if err := waitPodRunning(context.Background(), io.Discard, fake, "openbao", "openbao-0", time.Minute); err != nil {
		t.Fatalf("expected nil once Running, got %v", err)
	}
	if calls < 3 {
		t.Fatalf("expected the poll loop to retry until Running, got %d calls", calls)
	}
}

func TestWaitPodRunning_TimesOut(t *testing.T) {
	origAfter := timeAfter
	timeAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	defer func() { timeAfter = origAfter }()

	fake := fakeGRF(func(_ context.Context, _, _, _, _, _ string, _ ...string) (string, error) {
		return "Pending", nil // never Running
	})

	// budget 0 → deadline is now, so the first not-Running poll times out.
	err := waitPodRunning(context.Background(), io.Discard, fake, "openbao", "openbao-0", 0)
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
}

func TestWaitPodRunning_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	fake := fakeGRF(func(_ context.Context, _, _, _, _, _ string, _ ...string) (string, error) {
		return "Pending", nil
	})
	if err := waitPodRunning(ctx, io.Discard, fake, "openbao", "openbao-0", time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
