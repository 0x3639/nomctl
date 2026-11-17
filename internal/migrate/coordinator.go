package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/doctor"
	"github.com/0x3639/nomctl/internal/health"
	"github.com/0x3639/nomctl/internal/recovery"
	"github.com/0x3639/nomctl/internal/walletbackup"
)

type Coordinator struct {
	Local    *Endpoint
	Dial     func(context.Context, Connection) (Remote, error)
	Transfer Transfer
	Recovery func(context.Context, config.Config, string) (string, string, string, error)
}

func New() *Coordinator {
	return &Coordinator{Local: LocalEndpoint(), Dial: Connect, Transfer: Rsync, Recovery: createRecovery}
}

func createRecovery(ctx context.Context, cfg config.Config, passphrase string) (pointID, walletPath, walletHash string, err error) {
	if passphrase == "" {
		return "", "", "", errors.New("an encryption passphrase is required for the separate wallet/config backup")
	}
	archive, err := backup.RunRestorePoint(cfg, "Before device migration")
	if err != nil {
		return "", "", "", err
	}
	pointID = filepath.Base(filepath.Dir(archive))
	target, err := recovery.Point(cfg, pointID)
	if err != nil {
		return "", "", "", err
	}
	verified := recovery.Verify(ctx, cfg, target, recovery.Options{})
	if verified.Outcome != "passed" {
		return "", "", "", errors.New("pre-migration restore point did not pass verification")
	}
	wallet, err := walletbackup.Create(cfg, walletbackup.Options{Passphrase: passphrase})
	if err != nil {
		return "", "", "", err
	}
	plain, err := walletbackup.Open(wallet.Path, passphrase)
	if err != nil {
		return "", "", "", errors.New("encrypted wallet backup could not be verified")
	}
	defer clear(plain)
	if _, err := walletbackup.Inspect(plain); err != nil {
		return "", "", "", errors.New("wallet backup contents could not be verified")
	}
	walletHash, err = hashFile(ctx, wallet.Path)
	return pointID, wallet.Path, walletHash, err
}

func (c *Coordinator) Plan(ctx context.Context, cfg config.Config, conn Connection, destination Installation) (Plan, error) {
	l, err := c.Local.locked("plan device migration")
	if err != nil {
		return Plan{}, err
	}
	defer l.Release()
	if _, err := c.Local.Status(); !errors.Is(err, os.ErrNotExist) {
		return Plan{}, errors.New("this host already has a migration plan; inspect status and resume or return")
	}
	source := InstallationFor(cfg)
	if err := source.Validate(); err != nil {
		return Plan{}, err
	}
	if err := destination.Validate(); err != nil {
		return Plan{}, err
	}
	if err := conn.Validate(); err != nil {
		return Plan{}, err
	}
	if !validPath(cfg.BackupDir) || within(source.Data, cfg.BackupDir) {
		return Plan{}, errors.New("migration recovery backups must be outside the source data directory")
	}
	if err := c.Local.Runtime.Preflight(ctx, source, true); err != nil {
		return Plan{}, err
	}
	tree, err := Scan(ctx, source.Data, false)
	if err != nil {
		return Plan{}, err
	}
	settings, err := doctor.ReadLocalSettings(source.Data)
	if err != nil {
		return Plan{}, err
	}
	producer, err := validateProducer(source.Data)
	if err != nil {
		return Plan{}, err
	}
	id, err := NewID()
	if err != nil {
		return Plan{}, err
	}
	host, err := c.Local.Runtime.HostID(ctx)
	if err != nil {
		return Plan{}, err
	}
	binaryHash, err := hashFile(ctx, source.Binary)
	if err != nil {
		return Plan{}, err
	}
	binaryInfo, err := os.Stat(source.Binary)
	if err != nil {
		return Plan{}, err
	}
	remote, err := c.Dial(ctx, conn)
	if err != nil {
		return Plan{}, err
	}
	defer func() { _ = remote.Close() }()
	hello, err := remote.Call(ctx, Request{Action: "hello"})
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Protocol: Protocol, ID: id, Created: time.Now().UTC(), SourceHost: host, DestinationHost: hello.Host, Architecture: runtime.GOARCH, Source: source, Destination: destination, Connection: conn, BinarySHA256: binaryHash, Bytes: tree.Bytes, LargestFile: tree.LargestFile, BinaryBytes: binaryInfo.Size(), Entries: tree.Entries, BackupDirectory: cfg.BackupDir, MinFreeSpaceKB: cfg.MinFreeSpaceKB, Settings: settings, Producer: producer}
	p.BinaryVersion, p.BinaryCommit = backup.BinaryInfo(source.Binary)
	if hello.Protocol != Protocol || hello.Architecture != p.Architecture {
		return Plan{}, errors.New("destination must use the same architecture and a compatible migration-capable nomctl")
	}
	if err := p.Validate(); err != nil {
		return Plan{}, err
	}
	if _, err := remote.Call(ctx, Request{Action: "preview", Plan: p}); err != nil {
		return Plan{}, err
	}
	if err := saveState(c.Local.Dir, State{Plan: p, Role: "source", Phase: "planned"}); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// Copy transfers/resumes but never starts either producer. Confirmation of the
// handoff is a separate command, even after an entirely successful copy.
func (c *Coordinator) Copy(ctx context.Context, passphrase string, out io.Writer) (State, error) {
	l, err := c.Local.locked("migrate entire node data")
	if err != nil {
		return State{}, err
	}
	defer l.Release()
	s, err := c.Local.Status()
	if err != nil {
		return s, err
	}
	p := s.Plan
	if _, err := c.Local.bound(ctx, p, "source"); err != nil {
		return s, err
	}
	if s.Phase != "planned" && s.Phase != "protected" && s.Phase != "copying" && s.Phase != "verifying" && s.Phase != "ready" {
		return s, fmt.Errorf("phase %s cannot transfer; inspect status or use handoff/return", s.Phase)
	}
	remote, err := c.Dial(ctx, p.Connection)
	if err != nil {
		return s, err
	}
	defer func() { _ = remote.Close() }()
	if s.Phase == "planned" {
		if err := c.Local.Runtime.Preflight(ctx, p.Source, true); err != nil {
			return s, err
		}
		if _, err := remote.Call(ctx, Request{Action: "preview", Plan: p}); err != nil {
			return s, err
		}
		cfg := p.Source.Config()
		cfg.BackupDir = p.BackupDirectory
		cfg.MinFreeSpaceKB = p.MinFreeSpaceKB
		cfg.RestorePointKeepDays = 0
		s.RecoveryID, s.WalletBackup, s.WalletSHA256, err = c.Recovery(ctx, cfg, passphrase)
		if err != nil {
			return s, err
		}
		s.Phase = "protected"
		if err := saveState(c.Local.Dir, s); err != nil {
			return s, err
		}
	}
	if s.Phase == "protected" {
		settings, err := doctor.ReadLocalSettings(p.Source.Data)
		if err != nil || settings != p.Settings {
			return s, errors.New("source RPC or peer configuration changed after preview")
		}
		address, err := validateProducer(p.Source.Data)
		if err != nil || address != p.Producer {
			return s, errors.New("source producer configuration changed after preview")
		}
		if err := c.Local.Runtime.Block(ctx, p.Source, p.ID); err != nil {
			return s, err
		}
		s.Phase = "copying"
		if err := saveState(c.Local.Dir, s); err != nil {
			return s, err
		}
	}
	if err := c.Local.Runtime.Blocked(ctx, p.Source, p.ID); err != nil {
		return s, err
	}
	if err := c.Local.Runtime.Stopped(ctx, p.Source); err != nil {
		return s, err
	}
	if binaryHash, err := hashFile(ctx, p.Source.Binary); err != nil || binaryHash != p.BinarySHA256 {
		return s, errors.New("source znnd changed after the preview")
	}
	tree, err := Scan(ctx, p.Source.Data, true)
	if err != nil {
		return s, err
	}
	if s.Digest != "" && s.Digest != tree.Digest {
		return s, errors.New("stopped source contents changed; inspect the retained files before proceeding")
	}
	if s.Digest == "" {
		s.Plan.Bytes, s.Plan.Entries, s.Plan.LargestFile = tree.Bytes, tree.Entries, tree.LargestFile
		p = s.Plan
	}
	s.Digest = tree.Digest
	if err := saveState(c.Local.Dir, s); err != nil {
		return s, err
	}
	if s.Phase == "copying" {
		if _, err := remote.Call(ctx, Request{Action: "prepare", Plan: p}); err != nil {
			return s, err
		}
		fmt.Fprintln(out, "Transferring the entire data directory; both nodes remain stopped. Interrupted transfers can be resumed.")
		if err := c.Transfer(ctx, p, out); err != nil {
			return s, err
		}
		s.Phase = "verifying"
		if err := saveState(c.Local.Dir, s); err != nil {
			return s, err
		}
	}
	if s.Phase == "verifying" {
		// Check the source again after transfer; no changed snapshot is accepted.
		again, err := Scan(ctx, p.Source.Data, true)
		if err != nil || again.Digest != s.Digest {
			return s, errors.New("source changed during transfer; destination will not start")
		}
		status, statusErr := remote.Call(ctx, Request{Action: "status", Plan: p})
		if statusErr != nil {
			return s, statusErr
		}
		if status.State == nil {
			return s, errors.New("destination state unavailable")
		}
		if status.State.Phase == "copying" || status.State.Phase == "verified" {
			if _, err := remote.Call(ctx, Request{Action: "verify", Plan: p, Digest: s.Digest}); err != nil {
				s.Phase = "copying"
				return s, errors.Join(err, saveState(c.Local.Dir, s))
			}
		}
		if _, err := remote.Call(ctx, Request{Action: "promote", Plan: p}); err != nil {
			return s, err
		}
		s.Phase = "ready"
		if err := saveState(c.Local.Dir, s); err != nil {
			return s, err
		}
	}
	fmt.Fprintln(out, "Transfer verified. Both nodes remain stopped. Review status, then explicitly confirm migration handoff to start the new server.")
	return s, nil
}

func (c *Coordinator) Handoff(ctx context.Context) (State, error) {
	l, err := c.Local.locked("confirmed migration handoff")
	if err != nil {
		return State{}, err
	}
	defer l.Release()
	s, err := c.Local.Status()
	if err != nil {
		return s, err
	}
	p := s.Plan
	if _, err := c.Local.bound(ctx, p, "source"); err != nil {
		return s, err
	}
	if s.Phase != "ready" && s.Phase != "handing-off" && s.Phase != "complete" && s.Phase != "health-failed" {
		return s, errors.New("migration is not ready for handoff")
	}
	if err := c.Local.Runtime.Blocked(ctx, p.Source, p.ID); err != nil {
		return s, err
	}
	if err := c.Local.Runtime.Stopped(ctx, p.Source); err != nil {
		return s, err
	}
	remote, err := c.Dial(ctx, p.Connection)
	if err != nil {
		return s, err
	}
	defer func() { _ = remote.Close() }()
	s.Phase = "handing-off"
	if err := saveState(c.Local.Dir, s); err != nil {
		return s, err
	}
	response, startErr := remote.Call(ctx, Request{Action: "start", Plan: p})
	s.Phase = "health-failed"
	s.Health = response.Health
	if startErr == nil {
		s.Phase = "complete"
	}
	if err := saveState(c.Local.Dir, s); err != nil {
		return s, err
	}
	return s, startErr
}

func (c *Coordinator) Return(ctx context.Context) (State, error) {
	l, err := c.Local.locked("confirmed migration return to original")
	if err != nil {
		return State{}, err
	}
	defer l.Release()
	s, err := c.Local.Status()
	if err != nil {
		return s, err
	}
	p := s.Plan
	if _, err := c.Local.bound(ctx, p, "source"); err != nil {
		return s, err
	}
	if s.Phase == "planned" {
		return s, errors.New("no handoff has begun; the source was never blocked")
	}
	if s.Phase == "returned" {
		if s.Health == nil || !s.Health.Passed() {
			return s, errors.New("original node was returned but health is unconfirmed; run nomctl health")
		}
		return s, nil
	}
	remote, err := c.Dial(ctx, p.Connection)
	if err != nil {
		return s, err
	}
	defer func() { _ = remote.Close() }()
	response, err := remote.Call(ctx, Request{Action: "block-return", Plan: p})
	if err != nil {
		return s, err
	}
	if response.State == nil || response.State.Phase != "returned" {
		return s, errors.New("replacement is not confirmed persistently stopped")
	}
	s.Phase = "returning"
	if err := saveState(c.Local.Dir, s); err != nil {
		return s, err
	}
	if err := c.Local.Runtime.Block(ctx, p.Source, p.ID); err != nil {
		return s, err
	}
	if err := c.Local.Runtime.Unblock(ctx, p.Source, p.ID); err != nil {
		return s, err
	}
	if err := c.Local.Runtime.Start(ctx, p.Source); err != nil {
		return s, err
	}
	r, healthErr := c.Local.Runtime.Health(ctx, p.Source, p.BinarySHA256)
	s.Health = &r
	s.Phase = "returned"
	if err := saveState(c.Local.Dir, s); err != nil {
		return s, err
	}
	return s, healthErr
}

func WritePlan(out io.Writer, p Plan) {
	fmt.Fprintf(out, "Migration %s\nSource: %s (%s)\nDestination: %s:%s (%s)\nData: %.2f GiB across %d entries\nznnd version: %s\nCommit: %s\nSHA-256: %s\nArchitecture: %s; P2P port: %d; RPC: %s\n", p.ID, p.Source.Data, p.SourceHost, p.Connection.Host, p.Destination.Data, p.DestinationHost, float64(p.Bytes)/(1<<30), p.Entries, p.BinaryVersion, p.BinaryCommit, p.BinarySHA256, p.Architecture, p.Settings.P2PPort, p.Settings.RPCURL)
	fmt.Fprintln(out, "Downtime includes the full rsync transfer and content verification. Duration depends on data size, disk speed and network throughput. A protected restore point and encrypted wallet/config backup are required first.")
	fmt.Fprintln(out, FollowUp)
}
func WriteState(out io.Writer, s State) {
	fmt.Fprintf(out, "Migration %s: role=%s phase=%s\nOriginal: %s\nReplacement: %s:%s\nRecovery point: %s\nEncrypted wallet/config backup: %s\n", s.Plan.ID, s.Role, s.Phase, s.Plan.Source.Data, s.Plan.Connection.Host, s.Plan.Destination.Data, s.RecoveryID, s.WalletBackup)
	if s.Health != nil {
		health.Write(out, *s.Health)
	}
	fmt.Fprintln(out, "Use migrate resume to continue copying; migrate handoff to explicitly start the replacement; migrate return to first block the replacement and then restart the original.")
}
