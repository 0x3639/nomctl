// Package support builds a shareable diagnostics bundle for a node, a port of
// the collect-znnd-crash.sh script with node RPC and nomctl state added.
package support

import (
	"bufio"
	"encoding/json"
	"io"
	"regexp"
)

var (
	// reAssign matches password=..., token: ... and friends.
	reAssign = regexp.MustCompile(`(?i)((password|passwd|token|secret|api[-_]?key|authorization|mnemonic|private[-_]?key)[[:alnum:]_.-]*[=:][ \t]*)("[^"]*"|'[^']*'|[^[:space:]"']+)`)
	// reEnv matches systemd Environment= settings carrying secrets.
	reEnv = regexp.MustCompile(`(?i)(Environment="?[^" ]*(PASSWORD|TOKEN|SECRET|API_KEY|PRIVATE_KEY|MNEMONIC)=)[^" ]+`)
)

// Redact masks secret-looking assignments, matching the script's sed rules.
func Redact(s string) string {
	s = reAssign.ReplaceAllString(s, "${1}<redacted>")
	return reEnv.ReplaceAllString(s, "${1}<redacted>")
}

// CrashMarkers matches lines worth reading first after a crash.
var CrashMarkers = regexp.MustCompile(`(?i)panic|fatal|runtime:|goroutine|deadlock|segmentation|signal|out of memory|oom|killed process|memory cgroup|too many open files|no space left|corrupt|leveldb|resource temporarily unavailable|control process exited|main process exited|failed with result|scheduled restart|watchdog|stack trace`)

// FilterCrashMarkers copies the lines of r that match CrashMarkers to w.
func FilterCrashMarkers(r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		if CrashMarkers.Match(sc.Bytes()) {
			if _, err := w.Write(append(sc.Bytes(), '\n')); err != nil {
				return err
			}
		}
	}
	return sc.Err()
}

// RedactPeers replaces every peer IP (and self) in a stats.networkInfo
// document with "<redacted>", keeping counts, keys and names.
func RedactPeers(networkInfo []byte) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(networkInfo, &doc); err != nil {
		return nil, err
	}
	if peers, ok := doc["peers"].([]any); ok {
		for _, p := range peers {
			if m, ok := p.(map[string]any); ok {
				m["ip"] = "<redacted>"
			}
		}
	}
	if self, ok := doc["self"].(map[string]any); ok {
		self["ip"] = "<redacted>"
	}
	return json.MarshalIndent(doc, "", "  ")
}
