package backup

import (
	"context"
	"debug/buildinfo"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/execx"
)

// savedBinaryInfo reads identity from the exact saved znnd, never from a live
// RPC endpoint that may refer to another node. Failure is informational only.
func savedBinaryInfo(path string) (version, commit string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := execx.New(path, "version").Context(ctx).OutputLimited(16 << 10)
	if err == nil {
		version, commit = parseBinaryInfo(output)
	}
	// Go build metadata can retain a source revision when the command is
	// unavailable (including a noexec backup filesystem).
	if info, err := buildinfo.ReadFile(path); err == nil {
		if version == "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
		for _, setting := range info.Settings {
			if commit == "" && setting.Key == "vcs.revision" {
				commit = setting.Value
			}
		}
	}
	return version, commit
}

func parseBinaryInfo(output string) (version, commit string) {
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		// Version metadata is shown in terminal prompts. Accept a bounded,
		// printable identifier rather than arbitrary command output.
		if value == "" || len(value) > 128 || strings.IndexFunc(value, func(r rune) bool { return r < 32 || r > 126 }) >= 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "version":
			version = value
		case "commit hash", "git-commit-hash":
			commit = value
		}
	}
	return version, commit
}
