package metrics

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/execx"
)

// ServiceProps is the subset of `systemctl show` nomctl uses.
type ServiceProps struct {
	ActiveState   string
	SubState      string
	Result        string
	ControlGroup  string
	MainPID       int
	NRestarts     int
	ExecMainStart time.Time
}

// serviceProperties is the property list requested from systemctl.
var serviceProperties = []string{"ActiveState", "SubState", "Result", "ControlGroup", "MainPID", "NRestarts", "ExecMainStartTimestamp"}

// ReadServiceProps runs systemctl show for the unit.
func ReadServiceProps(unit string) (ServiceProps, error) {
	return ReadServicePropsContext(context.Background(), unit)
}

// ReadServicePropsContext bounds the status query by its caller and a short
// timeout, so an unavailable service manager cannot stall a diagnostic sample.
func ReadServicePropsContext(ctx context.Context, unit string) (ServiceProps, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"show", unit}
	for _, p := range serviceProperties {
		args = append(args, "-p", p)
	}
	out, err := execx.New("systemctl", args...).Context(ctx).OutputLimited(64 << 10)
	if err != nil {
		return ServiceProps{}, err
	}
	return ParseServiceProps(out), nil
}

// ParseServiceProps parses key=value lines as printed by systemctl show.
func ParseServiceProps(show string) ServiceProps {
	var p ServiceProps
	for _, line := range strings.Split(show, "\n") {
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "ActiveState":
			p.ActiveState = val
		case "SubState":
			p.SubState = val
		case "Result":
			p.Result = val
		case "ControlGroup":
			p.ControlGroup = val
		case "MainPID":
			p.MainPID, _ = strconv.Atoi(val)
		case "NRestarts":
			p.NRestarts, _ = strconv.Atoi(val)
		case "ExecMainStartTimestamp":
			p.ExecMainStart = parseSystemdTime(val)
		}
	}
	return p
}

// parseSystemdTime parses "Sat 2026-09-06 10:00:00 UTC" style timestamps.
func parseSystemdTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{"Mon 2006-01-02 15:04:05 MST", "2006-01-02 15:04:05 MST"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
