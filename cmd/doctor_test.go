package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/doctor"
)

func TestDoctorJSONAndExternalContactDisclosure(t *testing.T) {
	var out, stderr bytes.Buffer
	c := newDoctorCommand(func(_ context.Context, _ config.Config, o doctor.Options) (doctor.Report, error) {
		if o.Wait != time.Second || !strings.Contains(stderr.String(), "https://probe.example/check") || !strings.Contains(stderr.String(), "203.0.113.2") {
			t.Fatal("probe was not disclosed before collection", o, stderr.String())
		}
		return doctor.Report{SchemaVersion: 1, Identity: doctor.Identity{Version: "v0.0.10", Commit: "abc123"}}, nil
	})
	c.SetOut(&out)
	c.SetErr(&stderr)
	c.SetArgs([]string{"--json", "--wait", "1s", "--probe-url", "https://probe.example/check", "--public-ip", "203.0.113.2"})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	var r doctor.Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.Identity.Commit != "abc123" {
		t.Fatal(err, out.String())
	}
	if c.Annotations[annotationOptionalServiceTools] != "true" {
		t.Fatal("missing tools must not prevent diagnostics")
	}
}

func TestDoctorInvalidOptionsDoNotCollect(t *testing.T) {
	for _, args := range [][]string{{"--wait", "0s"}, {"--wait", "3m"}, {"--probe-url", "https://probe.example"}, {"unexpected"}} {
		c := newDoctorCommand(func(context.Context, config.Config, doctor.Options) (doctor.Report, error) {
			t.Fatal("collected for invalid options")
			return doctor.Report{}, nil
		})
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		c.SetArgs(args)
		if err := c.Execute(); err == nil {
			t.Fatal(args)
		}
	}
}
