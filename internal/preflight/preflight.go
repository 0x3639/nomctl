// Package preflight ports lib/preflight.sh: hardware, NTP and connectivity
// checks that run before any privileged operation.
package preflight

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/logx"
)

const (
	minCores      = 4
	minTotalGiB   = 4
	recommendGiB  = 2
	ntpServer     = "time.cloudflare.com"
	timesyncdConf = "/etc/systemd/timesyncd.conf"
)

// Run executes every check and returns the first hard failure.
func Run() error {
	if err := CPU(); err != nil {
		return err
	}
	if err := Memory(); err != nil {
		return err
	}
	if err := NTP(timesyncdConf); err != nil {
		return err
	}
	return Internet()
}

// CPU fails when fewer than four cores are available.
func CPU() error {
	cores := runtime.NumCPU()
	if cores < minCores {
		return fmt.Errorf("only %d CPU cores detected; minimum %d required", cores, minCores)
	}
	logx.Success(fmt.Sprintf("CPU check passed (%d cores)", cores))
	return nil
}

// Memory fails below 4 GiB total RAM and warns below 2 GiB available.
func Memory() error {
	total, avail, err := readMeminfo("/proc/meminfo")
	if err != nil {
		if runtime.GOOS != "linux" {
			slog.Warn("memory check skipped: not running on Linux")
			return nil
		}
		return fmt.Errorf("read memory info: %w", err)
	}
	totalGiB := total / 1024 / 1024
	availGiB := avail / 1024 / 1024
	if totalGiB < minTotalGiB {
		return fmt.Errorf("total RAM %dGiB detected; minimum %dGiB required", totalGiB, minTotalGiB)
	}
	if availGiB < recommendGiB {
		slog.Warn(fmt.Sprintf("only %dGiB free memory available; %dGiB recommended", availGiB, recommendGiB))
	}
	logx.Success(fmt.Sprintf("Memory check passed (%dGiB total, %dGiB free)", totalGiB, availGiB))
	return nil
}

func readMeminfo(path string) (totalKB, availKB int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "memtotal:":
			totalKB, _ = strconv.ParseInt(fields[1], 10, 64)
		case "memavailable:":
			availKB, _ = strconv.ParseInt(fields[1], 10, 64)
		}
	}
	if totalKB == 0 {
		return 0, 0, errors.New("MemTotal not found")
	}
	return totalKB, availKB, sc.Err()
}

// NTP makes sure systemd-timesyncd points at time.cloudflare.com, creating
// or patching the config file and restarting the service when it changes.
func NTP(confPath string) error {
	data, err := os.ReadFile(confPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", confPath, err)
	}
	if errors.Is(err, os.ErrNotExist) {
		slog.Info("timesyncd.conf not found; creating default file")
		data = nil
	}
	updated, changed := PatchTimesyncd(string(data))
	if !changed {
		logx.Success("NTP already configured (" + ntpServer + ")")
		return nil
	}
	slog.Info("Setting Cloudflare NTP server in timesyncd.conf")
	if err := os.WriteFile(confPath, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", confPath, err)
	}
	if err := execx.Run("systemctl", "restart", "systemd-timesyncd.service"); err != nil {
		return fmt.Errorf("restart systemd-timesyncd: %w", err)
	}
	logx.Success("NTP configuration applied and timesyncd restarted")
	return nil
}

var (
	reTimeSection = regexp.MustCompile(`(?i)^\s*\[Time\]\s*$`)
	reNTPLine     = regexp.MustCompile(`(?i)^\s*NTP=`)
	reNTPWanted   = regexp.MustCompile(`(?i)^\s*NTP=\s*` + regexp.QuoteMeta(ntpServer) + `\s*$`)
)

// PatchTimesyncd returns the timesyncd.conf content with NTP=time.cloudflare.com
// set under the [Time] section, and whether anything changed. It follows the
// bash logic: add a [Time] section if missing, drop every existing NTP= line,
// then insert the wanted line right after [Time].
func PatchTimesyncd(content string) (string, bool) {
	if strings.TrimSpace(content) == "" {
		return "[Time]\nNTP=" + ntpServer + "\n", true
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	hasSection := false
	for _, l := range lines {
		if reTimeSection.MatchString(l) {
			hasSection = true
		}
		if reNTPWanted.MatchString(l) && hasSection {
			return content, false
		}
	}
	if !hasSection {
		lines = append(lines, "", "[Time]")
	}
	out := make([]string, 0, len(lines)+1)
	for _, l := range lines {
		if reNTPLine.MatchString(l) {
			continue
		}
		out = append(out, l)
		if reTimeSection.MatchString(l) {
			out = append(out, "NTP="+ntpServer)
		}
	}
	return strings.Join(out, "\n") + "\n", true
}

// Internet fails when neither a TCP connection to 1.1.1.1 nor an HTTPS
// request to Google's connectivity endpoint succeeds.
func Internet() error {
	if c, err := net.DialTimeout("tcp", "1.1.1.1:443", 2*time.Second); err == nil {
		_ = c.Close()
		logx.Success("Internet connectivity")
		return nil
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("https://www.google.com/generate_204")
	if err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode < 400 {
			logx.Success("Internet connectivity")
			return nil
		}
	}
	return errors.New("no outbound Internet connectivity detected")
}
