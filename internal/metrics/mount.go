package metrics

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// MountPoint returns the mount point holding path according to mountinfo
// (the longest mount whose path is a prefix of path). It never touches
// path itself, so a directory the caller cannot traverse, such as
// /root/.znn seen from an unprivileged user, still resolves.
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
		// mountinfo: id parent major:minor root mountpoint options ...
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
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
