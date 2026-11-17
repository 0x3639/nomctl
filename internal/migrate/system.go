package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/deploy"
	"github.com/0x3639/nomctl/internal/execx"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/health"
	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/service"
)

type systemRuntime struct{ stateDir, unitDir string }

func (r systemRuntime) stateDirectory() string {
	if r.stateDir != "" {
		return r.stateDir
	}
	return StateDir
}
func (r systemRuntime) guardFile() string { return filepath.Join(r.stateDirectory(), "blocked") }
func (r systemRuntime) unitPath(i Installation) string {
	if r.unitDir != "" {
		return filepath.Join(r.unitDir, i.Service+".service")
	}
	return i.Config().ServiceUnitPath()
}

func (r systemRuntime) HostID(context.Context) (string, error) {
	data, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if len(value) != 32 {
		return "", errors.New("valid machine-id is required")
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:]), nil
}
func canonicalParent(path string) error {
	resolved, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return err
	}
	if resolved != filepath.Dir(path) {
		return errors.New("migration paths must use real, canonical parent directories")
	}
	return nil
}

func (r systemRuntime) Preflight(ctx context.Context, i Installation, source bool) error {
	if err := i.Validate(); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return errors.New("migration requires root on both hosts")
	}
	for _, tool := range []string{"rsync", "systemctl"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s is required on both hosts", tool)
		}
	}
	if err := canonicalParent(i.Data); err != nil {
		return err
	}
	if err := canonicalParent(i.Binary); err != nil {
		return err
	}
	if source {
		observation := health.Observe(ctx, i.Config())
		switch observation.Sample.Service.ActiveState {
		case "active":
			identity := observation.Identity
			if identity.Installed.SHA256 == "" || identity.Running.SHA256 != identity.Installed.SHA256 {
				return errors.New("installed znnd does not match the running executable; resolve its identity before migration")
			}
		case "":
			return errors.New("source service state could not be confirmed")
		}
		unit, err := os.ReadFile(r.unitPath(i))
		if err != nil {
			return err
		}
		if string(unit) != deploy.UnitFile(i.Config()) {
			return errors.New("source requires the standard nomctl-managed root service; review custom service settings before migration")
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		dropins, err := execx.New("systemctl", "show", "--property=DropInPaths", "--value", i.Service).Context(ctx).OutputLimited(16 << 10)
		if err != nil || strings.TrimSpace(dropins) != "" {
			return errors.New("source service has overrides or cannot be inspected; review them before migration")
		}
		return nil
	}
	for _, path := range []string{i.Data, i.Binary, r.unitPath(i), r.unitPath(i) + ".d"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("destination path already exists or cannot be checked: %s", path)
		}
	}
	st, err := service.Status(i.Service)
	if err != nil {
		return err
	}
	if st != service.NotFound {
		return errors.New("destination already has a node service; choose an unused service and data path")
	}
	return nil
}

func (r systemRuntime) guardDropin(i Installation) string {
	return r.unitPath(i) + ".d/90-nomctl-migration.conf"
}
func (r systemRuntime) guardContent() string {
	return "[Unit]\nConditionPathExists=!" + r.guardFile() + "\n"
}

func (r systemRuntime) Block(ctx context.Context, i Installation, id string) error {
	if !idPattern.MatchString(id) {
		return errors.New("invalid migration ID")
	}
	if err := privateDir(r.stateDirectory()); err != nil {
		return err
	}
	if old, err := fsx.ReadRuntimeFile(r.guardFile(), 128); err == nil && strings.TrimSpace(string(old)) != id {
		return errors.New("another migration owns the node guard")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := fsx.WriteRuntimeFile(r.guardFile(), []byte(id+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.guardDropin(i)), 0o755); err != nil {
		return err
	}
	if err := fsx.WriteRuntimeFile(r.guardDropin(i), []byte(r.guardContent()), 0o644); err != nil {
		return err
	}
	if err := service.DaemonReload(); err != nil {
		return err
	}
	st, err := service.Status(i.Service)
	if err != nil {
		return err
	}
	if st != service.NotFound {
		if err := service.Disable(i.Service); err != nil {
			return err
		}
	}
	return r.Stopped(ctx, i)
}

func (r systemRuntime) Blocked(_ context.Context, i Installation, id string) error {
	data, err := fsx.ReadRuntimeFile(r.guardFile(), 128)
	if err != nil || strings.TrimSpace(string(data)) != id {
		return errors.New("persistent migration guard is missing or belongs to another migration")
	}
	dropin, err := fsx.ReadRuntimeFile(r.guardDropin(i), 4096)
	if err != nil || string(dropin) != r.guardContent() {
		return errors.New("persistent systemd start block is missing or changed")
	}
	return nil
}
func (r systemRuntime) Stop(_ context.Context, i Installation) error { return service.Stop(i.Service) }
func (r systemRuntime) Stopped(ctx context.Context, i Installation) error {
	st, err := service.Status(i.Service)
	if err != nil {
		return err
	}
	if !st.Stopped() {
		return fmt.Errorf("node is not confirmed stopped (%s)", st)
	}
	if st == service.NotFound {
		return nil
	}
	props, err := metrics.ReadServicePropsContext(ctx, i.Service)
	if err != nil {
		return err
	}
	if props.MainPID != 0 {
		return errors.New("node process remains after stopping")
	}
	return nil
}

func (r systemRuntime) Prepare(ctx context.Context, p Plan) error {
	i := p.Destination
	if err := r.Blocked(ctx, i, p.ID); err != nil {
		return err
	}
	if err := r.Stopped(ctx, i); err != nil {
		return err
	}
	staged := filepath.Join(p.Stage(), "node-binary")
	sum, err := hashFile(ctx, staged)
	if err != nil || sum != p.BinarySHA256 {
		return errors.New("transferred znnd checksum does not match the source")
	}
	if old, err := hashFile(ctx, i.Binary); err == nil {
		if old != sum {
			return errors.New("destination binary conflicts with this migration")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := installBinary(staged, i.Binary); err != nil {
			return err
		}
	} else {
		return err
	}
	want := deploy.UnitFile(i.Config())
	if old, err := os.ReadFile(r.unitPath(i)); err == nil && string(old) != want {
		return errors.New("destination service conflicts with this migration")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := fsx.WriteRuntimeFile(r.unitPath(i), []byte(want), 0o644); err != nil {
		return err
	}
	if err := service.DaemonReload(); err != nil {
		return err
	}
	return service.Disable(i.Service)
}

func (r systemRuntime) Unblock(ctx context.Context, i Installation, id string) error {
	if _, err := os.Lstat(r.guardFile()); errors.Is(err, os.ErrNotExist) {
		dropin, err := fsx.ReadRuntimeFile(r.guardDropin(i), 4096)
		if err != nil || string(dropin) != r.guardContent() {
			return errors.New("migration start condition is missing")
		}
		return nil
	}
	if err := r.Blocked(ctx, i, id); err != nil {
		return err
	}
	// Removing the sentinel is the last step that permits a start. The benign
	// condition remains installed and will block again if return recreates it.
	if err := os.Remove(r.guardFile()); err != nil {
		return err
	}
	f, err := os.Open(r.stateDirectory())
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (r systemRuntime) Start(_ context.Context, i Installation) error {
	if err := service.Enable(i.Service); err != nil {
		return err
	}
	return service.Start(i.Service)
}
func (r systemRuntime) Health(ctx context.Context, i Installation, expected string) (health.Report, error) {
	before := health.Observe(ctx, i.Config())
	return health.Check(ctx, i.Config(), before, expected, health.DefaultWindow)
}

func destinationSpace(p Plan, additional int64) error {
	cfg := p.Destination.Config()
	cfg.MinFreeSpaceKB = p.MinFreeSpaceKB
	return backup.CheckRestoreSpace(cfg, filepath.Dir(p.Destination.Data), additional)
}

func freePorts(settingsURL string, p2p int) error {
	u, err := url.Parse(settingsURL)
	if err != nil {
		return err
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return errors.New("migration requires a loopback or wildcard HTTP RPC bind address")
	}
	for _, addr := range []string{u.Host, net.JoinHostPort("", fmt.Sprint(p2p))} {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			return errors.New("destination RPC or P2P port is unavailable")
		}
		_ = listener.Close()
	}
	return nil
}

var checkDestinationSpace = destinationSpace
var checkDestinationPorts = freePorts

// A private temporary file plus atomic rename avoids partial executables after
// a crash; fsync makes the installed identity durable before handoff.
func installBinary(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(target), ".nomctl-migration-binary-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(out.Name()) }()
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Chmod(0755); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(out.Name(), target); err != nil {
		return err
	}
	return syncDir(filepath.Dir(target))
}

// Credit completed staged bytes on resume while retaining room for a full
// replacement of the largest file. Partial files consume disk but earn no
// credit; the live free-space guard still runs during the receiver.
func remainingSpace(p Plan) (int64, error) {
	var stored int64
	err := filepath.WalkDir(filepath.Join(p.Stage(), "data"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			stored += min(info.Size(), p.Bytes-stored)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	return p.Bytes - stored + p.LargestFile + p.BinaryBytes + p.Entries*(64<<10), nil
}
