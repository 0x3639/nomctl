package backup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Cron versions differ in whether they read /etc/timezone or /etc/localtime.
// Never choose one silently when the two configurations disagree. Login-shell
// TZ overrides do not tell us which timezone the scheduler daemon uses.
func serverLocation() (*time.Location, error) {
	return readServerLocation("/etc/timezone", "/etc/localtime", "/usr/share/zoneinfo")
}

func readServerLocation(timezonePath, localtimePath, zoneinfoDir string) (*time.Location, error) {
	zoneName, nameErr := os.ReadFile(timezonePath)
	if nameErr != nil && !errors.Is(nameErr, os.ErrNotExist) {
		return nil, fmt.Errorf("read scheduler timezone %s: %w", timezonePath, nameErr)
	}
	localData, localErr := os.ReadFile(localtimePath)
	if localErr != nil && !errors.Is(localErr, os.ErrNotExist) {
		return nil, fmt.Errorf("read scheduler timezone %s: %w", localtimePath, localErr)
	}
	local := time.UTC // Missing system timezone configuration means UTC.
	if localErr == nil {
		name := "server local"
		if path, err := filepath.EvalSymlinks(localtimePath); err == nil {
			if _, zone, ok := strings.Cut(path, "/zoneinfo/"); ok {
				name = zone
			}
		}
		var err error
		local, err = time.LoadLocationFromTZData(name, localData)
		if err != nil {
			return nil, fmt.Errorf("parse scheduler timezone %s: %w", localtimePath, err)
		}
	}
	if errors.Is(nameErr, os.ErrNotExist) {
		return local, nil
	}
	zone := strings.TrimSpace(string(zoneName))
	if zone == "" || zone == "Local" || filepath.IsAbs(zone) || strings.Contains(zone, "..") {
		return nil, fmt.Errorf("invalid scheduler timezone in %s: %q", timezonePath, zone)
	}
	namedData, err := os.ReadFile(filepath.Join(zoneinfoDir, zone))
	if err != nil {
		return nil, fmt.Errorf("load scheduler timezone %q from %s: %w", zone, timezonePath, err)
	}
	named, err := time.LoadLocationFromTZData(zone, namedData)
	if err != nil {
		return nil, fmt.Errorf("parse scheduler timezone %q: %w", zone, err)
	}
	if localErr == nil && bytes.Equal(namedData, localData) {
		// This also accepts aliases pointing to identical timezone definitions.
		return named, nil
	}
	if errors.Is(localErr, os.ErrNotExist) {
		// A named fixed UTC zone agrees with the absent localtime default.
		at := time.Unix(0, 0).In(named)
		_, offset := at.Zone()
		start, end := at.ZoneBounds()
		if offset == 0 && start.IsZero() && end.IsZero() {
			return time.UTC, nil
		}
	}
	return nil, fmt.Errorf("%s (%s) and %s disagree; cron versions use different timezone sources, so next/missed slots are unavailable; align the timezone files and restart cron", timezonePath, zone, localtimePath)
}
