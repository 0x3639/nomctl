package restore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/doctor"
	"github.com/0x3639/nomctl/internal/health"
)

func TestHealthFailureRetainsPairWithoutAutomaticRestore(t *testing.T) {
	oldObserve, oldCheck, oldPreserve := observeChange, checkChange, preserveBeforeRestore
	t.Cleanup(func() { observeChange, checkChange, preserveBeforeRestore = oldObserve, oldCheck, oldPreserve })
	observeChange = func(context.Context, config.Config) doctor.NodeObservation {
		return doctor.NodeObservation{Problem: "before"}
	}
	preserved := false
	preserveBeforeRestore = func(config.Config) (string, error) { preserved = true; return "matching-pair", nil }
	checkChange = func(_ context.Context, _ config.Config, before doctor.NodeObservation, expected string, _ time.Duration) (health.Report, error) {
		if before.Problem != "before" || expected != "intended" {
			t.Fatal("lost before/after identity")
		}
		return health.Report{Outcome: "stalled"}, errors.New("no progress")
	}
	for _, dataOnly := range []bool{false, true} {
		changes := 0
		err := changeWithHealth(config.Default(), dataOnly, "intended", func() error {
			if !preserved {
				t.Fatal("changed before preserving")
			}
			changes++
			return nil
		})
		var failure *health.Failure
		if !errors.As(err, &failure) || failure.RecoveryID != "matching-pair" || failure.DataOnly != dataOnly || changes != 1 {
			t.Fatalf("changes=%d failure=%+v", changes, failure)
		}
	}
	preserveBeforeRestore = func(config.Config) (string, error) { return "", errors.New("disk full") }
	if err := changeWithHealth(config.Default(), false, "intended", func() error { t.Fatal("changed without recovery"); return nil }); err == nil {
		t.Fatal("expected refusal")
	}
}
