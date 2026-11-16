package backup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/service"
)

type ScheduleStatus struct {
	State          string        `json:"state"`
	Schedule       *Schedule     `json:"schedule,omitempty"`
	Timezone       string        `json:"timezone"`
	Scheduler      string        `json:"scheduler"`
	SchedulerError string        `json:"scheduler_error,omitempty"`
	NextRun        *time.Time    `json:"next_run,omitempty"`
	LastAttempt    *ScheduledRun `json:"last_attempt,omitempty"`
	LastSuccess    *ScheduledRun `json:"last_success,omitempty"`
	MissedRun      *time.Time    `json:"missed_run,omitempty"`
	HistoryNote    string        `json:"history_note,omitempty"`
}

func BackupStatus() (ScheduleStatus, error) {
	loc, err := serverLocation()
	if err != nil {
		return ScheduleStatus{}, err
	}
	s, err := scheduleStatus(CronPath, HistoryDir, time.Now().In(loc))
	if err != nil {
		return s, err
	}
	if s.Schedule == nil {
		exists, err := service.Exists(TimerName + ".timer")
		if err != nil {
			s.SchedulerError = err.Error()
		} else if exists {
			s.State, s.HistoryNote = "legacy timer", "Run 'nomctl backup schedule' to migrate the old timer and enable status controls."
		}
		return s, nil
	}
	unit, err := installedCronUnit()
	if err != nil {
		s.Scheduler, s.SchedulerError = "unknown", err.Error()
		return s, nil
	}
	if unit == "" {
		s.Scheduler = "missing"
		return s, nil
	}
	state, err := service.Status(unit)
	s.Scheduler = unit + ": " + state.String()
	if err != nil {
		s.SchedulerError = err.Error()
	}
	return s, nil
}

// Read the server's timezone file rather than a login shell's TZ override.
func serverLocation() (*time.Location, error) {
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		zone := strings.TrimSpace(string(data))
		if loc, err := time.LoadLocation(zone); err == nil && zone != "" && zone != "Local" {
			return loc, nil
		}
	}
	data, err := os.ReadFile("/etc/localtime")
	if err != nil {
		return nil, fmt.Errorf("read server timezone: %w", err)
	}
	name := "server local"
	if path, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if _, zone, ok := strings.Cut(path, "/zoneinfo/"); ok {
			name = zone
		}
	}
	return time.LoadLocationFromTZData(name, data)
}

func scheduleStatus(path, historyDir string, now time.Time) (ScheduleStatus, error) {
	s, err := readSchedule(path)
	if err != nil {
		return ScheduleStatus{}, err
	}
	zone, offset := now.Zone()
	result := ScheduleStatus{State: "not configured", Schedule: s, Timezone: fmt.Sprintf("%s (%s, UTC%+03d:%02d)", now.Location(), zone, offset/3600, abs(offset/60)%60), Scheduler: "not configured"}
	if s != nil {
		result.State = "paused"
		if s.Enabled {
			result.State = "enabled"
			next := dailySlot(*s, now, true)
			result.NextRun = &next
		}
		if s.Version == 0 {
			result.HistoryNote = "Legacy cron schedule: edit or pause/resume to enable outcome tracking; earlier outcomes are unknown."
		}
	}
	records, err := readHistory(historyDir)
	if err != nil {
		return result, err
	}
	for _, r := range records {
		// Do not let an old destination or service claim current protection.
		if s != nil && (r.run.Directory != s.Directory || r.run.Service != s.Service || r.run.DataDirectory != s.DataDirectory) {
			continue
		}
		copy := r.run
		if result.LastAttempt == nil {
			result.LastAttempt = &copy
		}
		if result.LastSuccess == nil && copy.Outcome == "success" {
			result.LastSuccess = &copy
		}
	}
	if s != nil && s.Enabled && s.Version == 1 {
		// A five-minute grace avoids declaring a run missed while cron starts it.
		expected := dailySlot(*s, now.Add(-5*time.Minute), false)
		if !expected.Before(s.Since) {
			attempted := false
			for _, r := range records {
				if r.run.ScheduleSince.Equal(s.Since) && !r.run.Started.Before(expected) {
					attempted = true
					break
				}
			}
			if !attempted {
				result.MissedRun = &expected
			}
		}
	}
	return result, nil
}

// dailySlot follows Debian/Ubuntu cron's fixed-time DST policy: run skipped
// times soon after the forward jump, and do not repeat the job on a backward
// jump. The daemon still controls actual dispatch (including manual clock jumps).
func dailySlot(s Schedule, now time.Time, next bool) time.Time {
	day := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	step := -1
	if next {
		step = 1
	}
	for i := 0; i < 4; i++ {
		slot := slotOnDay(s, day)
		if !slot.IsZero() && ((next && slot.After(now)) || (!next && !slot.After(now))) {
			return slot
		}
		day = day.AddDate(0, 0, step)
	}
	return time.Time{}
}

func slotOnDay(s Schedule, day time.Time) time.Time {
	year, month, date := day.Date()
	target := s.Hour*60 + s.Minute
	// Enumerate actual minutes around noon. Taking the first local minute at or
	// after the requested time handles both missing and repeated clock hours.
	for candidate := day.Add(-18 * time.Hour); candidate.Before(day.Add(18 * time.Hour)); candidate = candidate.Add(time.Minute) {
		y, m, d := candidate.Date()
		if y == year && m == month && d == date && candidate.Hour()*60+candidate.Minute() >= target {
			return candidate
		}
	}
	return time.Time{}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func WriteScheduleStatus(w io.Writer, s ScheduleStatus) {
	fmt.Fprintf(w, "Local backup schedule: %s\nServer timezone: %s\nScheduler: %s\n", s.State, s.Timezone, s.Scheduler)
	if s.SchedulerError != "" {
		fmt.Fprintln(w, "Scheduler warning:", s.SchedulerError)
	}
	if s.Schedule != nil {
		fmt.Fprintf(w, "Daily time: %02d:%02d (server time)\nRetention: %d days (maximum 30 archives; 75%% disk limit)\nDestination: %s\n", s.Schedule.Hour, s.Schedule.Minute, s.Schedule.RetentionDays, s.Schedule.Directory)
	}
	if s.NextRun != nil {
		fmt.Fprintln(w, "Next scheduled slot:", s.NextRun.Format(time.RFC3339))
	} else {
		fmt.Fprintln(w, "Next scheduled slot: none")
	}
	if s.LastAttempt == nil {
		fmt.Fprintln(w, "Last scheduled attempt: unknown (no recorded attempt)")
	} else {
		fmt.Fprintf(w, "Last scheduled attempt: %s — %s\n", s.LastAttempt.Started.Format(time.RFC3339), s.LastAttempt.Outcome)
		if s.LastAttempt.Reason != "" {
			fmt.Fprintln(w, "Reason:", s.LastAttempt.Reason)
		}
	}
	if s.LastSuccess == nil {
		fmt.Fprintln(w, "Last scheduled success: unknown (no recorded success)")
	} else {
		fmt.Fprintf(w, "Last scheduled success: %s\nArchive at completion: %s (may since have been pruned)\n", s.LastSuccess.Started.Format(time.RFC3339), s.LastSuccess.Archive)
	}
	if s.MissedRun != nil {
		fmt.Fprintln(w, "Missed scheduled slot:", s.MissedRun.Format(time.RFC3339), "(no attempt recorded)")
	}
	if s.HistoryNote != "" {
		fmt.Fprintln(w, s.HistoryNote)
	}
	fmt.Fprintln(w, "No catch-up or automatic retries. Enabled schedules wait for the next daily slot; cron controls daylight-saving adjustments.")
	fmt.Fprintln(w, "Protected restore points are separate and are not changed by schedule controls.")
}
