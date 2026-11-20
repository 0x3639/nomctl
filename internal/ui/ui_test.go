package ui

import (
	"errors"
	"testing"
)

func TestStepRunsOnceAndPreservesError(t *testing.T) {
	SetDebug(true)
	t.Cleanup(func() { SetDebug(false) })
	want := errors.New("operation failed")
	calls := 0
	got := Step("test operation", func() error { calls++; return want })
	if calls != 1 || !errors.Is(got, want) {
		t.Fatalf("calls=%d error=%v", calls, got)
	}
}
