package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/0x3639/nomctl/internal/metrics"
)

type Digest struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Identity struct {
	Version   string `json:"rpc_version,omitempty"`
	Commit    string `json:"rpc_commit,omitempty"`
	Installed Digest `json:"installed"`
	Running   Digest `json:"running"`
}

func collectIdentity(ctx context.Context, installed, proc string, svc metrics.ServiceSample, n metrics.NodeSample) Identity {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	i := Identity{Version: clean(n.Version), Commit: clean(n.Commit), Installed: digest(ctx, installed)}
	if svc.MainPID <= 0 {
		i.Running.Error = "no running service process was observed"
		return i
	}
	path := filepath.Join(proc, strconv.Itoa(svc.MainPID), "exe")
	before, err := metrics.ReadProcStat(proc, svc.MainPID)
	if err != nil || before.StartTime == 0 {
		i.Running = Digest{Path: path, Error: "process identity could not be read"}
		return i
	}
	i.Running = digest(ctx, path)
	if target, err := os.Readlink(path); err == nil {
		i.Running.Path = clean(target)
	}
	after, err := metrics.ReadProcStat(proc, svc.MainPID)
	props, serviceErr := metrics.ReadServicePropsContext(ctx, svc.Unit)
	if err != nil || serviceErr != nil || before.StartTime != after.StartTime || props.MainPID != svc.MainPID || !props.ExecMainStart.Equal(svc.Since) || props.NRestarts != svc.NRestarts {
		i.Running.SHA256, i.Running.Error = "", "service process changed or could not be rechecked during collection; repeat the check"
	}
	return i
}

func digest(ctx context.Context, path string) Digest {
	d := Digest{Path: clean(path)}
	hash, err := hashFile(ctx, path)
	if err != nil {
		// Do not print arbitrary filesystem/command error text as identity.
		d.Error = "executable could not be read as a stable regular file within diagnostic limits"
	} else {
		d.SHA256 = hash
	}
	return d
}

func hashFile(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", err
	}
	const maxSize = 1 << 30
	if !before.Mode().IsRegular() || before.Size() > maxSize {
		return "", errors.New("not a bounded regular executable")
	}
	h := sha256.New()
	buf := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := f.Read(buf)
		total += int64(n)
		if total > maxSize {
			return "", errors.New("executable exceeds size limit")
		}
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	after, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !os.SameFile(before, after) || before.Size() != total || after.Size() != total || !before.ModTime().Equal(after.ModTime()) {
		return "", errors.New("executable changed during hashing")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func identityCheck(i Identity) Check {
	evidence := fmt.Sprintf("RPC version %s; commit %s. SHA-256 values identify file contents; they do not verify a release signature.", known(i.Version), known(i.Commit))
	if i.Installed.SHA256 != "" && i.Running.SHA256 != "" && i.Installed.SHA256 != i.Running.SHA256 {
		return check("identity", "warning", "Running and installed znnd executables differ", evidence, "Check whether an upgrade is awaiting restart or the service uses another executable. Review the paths before restarting.")
	}
	if i.Version == "" || i.Commit == "" || i.Installed.SHA256 == "" || i.Running.SHA256 == "" {
		return check("identity", "unknown", "Some znnd identity evidence is unavailable", evidence, "Review the version/hash sources above, service executable path and RPC availability; no version was inferred from a filename.")
	}
	return check("identity", "ok", "znnd identity collected; running and installed hashes match", evidence, "")
}
