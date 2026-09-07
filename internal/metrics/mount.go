package metrics

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// MountPoint returns the mount point holding path according to mountinfo
// (the longest non-tmpfs mount whose path is a prefix of path). It never
// touches path itself, so a directory the caller cannot traverse still
// resolves. tmpfs mounts are skipped because a sandboxed service sees the
// inaccessible tmpfs systemd's ProtectHome places over /root, which says
// nothing about the disk underneath.
func MountPoint(mountinfo, path string) string {
	f, err := os.Open(mountinfo)
	if err != nil {
		return "/"
	}
	defer func() { _ = f.Close() }()
	clean := filepath.Clean(path)
	best := "/"
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// mountinfo: id parent major:minor root mountpoint options ... - fstype source ...
		line := sc.Text()
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if _, after, ok := strings.Cut(line, " - "); ok {
			if rest := strings.Fields(after); len(rest) > 0 && rest[0] == "tmpfs" {
				continue
			}
		}
		mp := unescapeMount(fields[4])
		if mp == clean || strings.HasPrefix(clean, strings.TrimSuffix(mp, "/")+"/") {
			if len(mp) > len(best) {
				best = mp
			}
		}
	}
	return best
}

// unescapeMount decodes the octal escapes mountinfo uses for spaces and
// the like (\040).
func unescapeMount(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, ok := octal(s[i+1 : i+4]); ok {
				b.WriteByte(v)
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func octal(s string) (byte, bool) {
	var v int
	for _, c := range s {
		if c < '0' || c > '7' {
			return 0, false
		}
		v = v*8 + int(c-'0')
	}
	return byte(v), true
}
