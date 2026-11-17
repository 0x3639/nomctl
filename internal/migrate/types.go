// Package migrate coordinates a stopped-node rsync handoff between two hosts.
package migrate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/doctor"
	"github.com/0x3639/nomctl/internal/fsx"
	"github.com/0x3639/nomctl/internal/health"
	"github.com/0x3639/nomctl/internal/lock"
)

const Protocol = 1
const StateDir = "/var/lib/nomctl/migration"
const FollowUp = "Original data is retained. Configure backup schedules, alerts and any orchestrator separately on the new host; external backup directories were not copied."

type Installation struct {
	Data    string `json:"data"`
	Binary  string `json:"binary"`
	Service string `json:"service"`
}

func InstallationFor(c config.Config) Installation {
	return Installation{Data: filepath.Clean(c.ZnnDir), Binary: filepath.Clean(c.BinaryPath()), Service: c.ServiceName}
}
func (i Installation) Config() config.Config {
	c := config.Default()
	c.ZnnDir = i.Data
	c.InstallDir = filepath.Dir(i.Binary)
	c.BinaryName = filepath.Base(i.Binary)
	c.ServiceName = i.Service
	return c
}

var unitPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,100}$`)
var hashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func validPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && !strings.ContainsAny(path, "\r\n\x00")
}
func (i Installation) Validate() error {
	if !validPath(i.Data) || !validPath(i.Binary) || !unitPattern.MatchString(i.Service) {
		return errors.New("migration requires absolute, clean paths and a simple systemd service name")
	}
	if within(i.Data, i.Binary) || within(i.Binary, i.Data) {
		return errors.New("node executable must be outside the data directory")
	}
	return nil
}

type Connection struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Nomctl string `json:"nomctl"`
	Sudo   bool   `json:"sudo"`
}

func (c Connection) Validate() error {
	if c.Host == "" || strings.HasPrefix(c.Host, "-") || strings.ContainsAny(c.Host, " \t\r\n\x00/'\";`$\\") || c.Port < 1 || c.Port > 65535 || !validPath(c.Nomctl) {
		return errors.New("invalid SSH destination, port, or remote nomctl path")
	}
	return nil
}

type Plan struct {
	Protocol        int                  `json:"protocol"`
	ID              string               `json:"id"`
	Created         time.Time            `json:"created"`
	SourceHost      string               `json:"source_host"`
	DestinationHost string               `json:"destination_host"`
	Architecture    string               `json:"architecture"`
	Source          Installation         `json:"source"`
	Destination     Installation         `json:"destination"`
	Connection      Connection           `json:"connection"`
	BinarySHA256    string               `json:"binary_sha256"`
	BinaryVersion   string               `json:"binary_version,omitempty"`
	BinaryCommit    string               `json:"binary_commit,omitempty"`
	LargestFile     int64                `json:"largest_file"`
	BinaryBytes     int64                `json:"binary_bytes"`
	Bytes           int64                `json:"bytes"`
	Entries         int64                `json:"entries"`
	MinFreeSpaceKB  int64                `json:"min_free_space_kb"`
	BackupDirectory string               `json:"backup_directory"`
	Settings        doctor.LocalSettings `json:"settings"`
	Producer        string               `json:"producer,omitempty"`
}

func (p Plan) Validate() error {
	if p.Protocol != Protocol || !idPattern.MatchString(p.ID) || p.SourceHost == "" || p.DestinationHost == "" || p.SourceHost == p.DestinationHost || p.Architecture != runtime.GOARCH || p.Bytes < 0 || p.LargestFile < 0 || p.LargestFile > p.Bytes || p.BinaryBytes < 0 || p.BinaryBytes > (1<<30) || p.Bytes > (math.MaxInt64-p.BinaryBytes)/2 || p.Entries < 0 || p.Entries > (math.MaxInt64-p.Bytes-p.LargestFile-p.BinaryBytes)/(64<<10) || p.MinFreeSpaceKB < 0 || !hashPattern.MatchString(p.BinarySHA256) || !validPath(p.BackupDirectory) {
		return errors.New("invalid or incompatible migration plan")
	}
	if p.Settings.P2PHost != "" {
		ip := net.ParseIP(p.Settings.P2PHost)
		if ip == nil || !ip.IsUnspecified() && !ip.IsLoopback() {
			return errors.New("use a wildcard or loopback P2P bind before migration; a device-specific address cannot be moved unchanged")
		}
	}
	if err := p.Connection.Validate(); err != nil {
		return err
	}
	if err := p.Source.Validate(); err != nil {
		return err
	}
	return p.Destination.Validate()
}
func (p Plan) Stage() string {
	return filepath.Join(filepath.Dir(p.Destination.Data), ".nomctl-migrate-"+p.ID)
}

type State struct {
	Plan         Plan           `json:"plan"`
	Role         string         `json:"role"`
	Phase        string         `json:"phase"`
	Digest       string         `json:"digest,omitempty"`
	RecoveryID   string         `json:"recovery_id,omitempty"`
	WalletBackup string         `json:"wallet_backup,omitempty"`
	WalletSHA256 string         `json:"wallet_sha256,omitempty"`
	Health       *health.Report `json:"health,omitempty"`
	Updated      time.Time      `json:"updated"`
}

func NewID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func loadState(dir string) (State, error) {
	var s State
	data, err := fsx.ReadRuntimeFile(filepath.Join(dir, "state.json"), 2<<20)
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(data, &s); err != nil {
		return s, err
	}
	if err = s.Plan.Validate(); err != nil {
		return s, err
	}
	if s.Role != "source" && s.Role != "destination" {
		return s, errors.New("invalid migration role")
	}
	return s, nil
}
func saveState(dir string, s State) error {
	s.Updated = time.Now().UTC()
	if err := privateDir(dir); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return fsx.WriteRuntimeFile(filepath.Join(dir, "state.json"), append(data, '\n'), 0o600)
}
func samePlan(a, b Plan) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(aa) == string(bb)
}
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

type Runtime interface {
	HostID(context.Context) (string, error)
	Preflight(context.Context, Installation, bool) error
	Block(context.Context, Installation, string) error
	Blocked(context.Context, Installation, string) error
	Stop(context.Context, Installation) error
	Stopped(context.Context, Installation) error
	Prepare(context.Context, Plan) error
	Unblock(context.Context, Installation, string) error
	Start(context.Context, Installation) error
	Health(context.Context, Installation, string) (health.Report, error)
}

type Endpoint struct {
	Dir, LockPath string
	Runtime       Runtime
}

func LocalEndpoint() *Endpoint {
	return &Endpoint{Dir: StateDir, LockPath: lock.DefaultPath, Runtime: systemRuntime{}}
}
func (e *Endpoint) Status() (State, error) { return loadState(e.Dir) }
func (e *Endpoint) locked(operation string) (*lock.Lock, error) {
	return lock.AcquireMigration(e.LockPath, operation)
}

func (e *Endpoint) bound(ctx context.Context, p Plan, role string) (State, error) {
	s, err := e.Status()
	if err != nil {
		return s, err
	}
	if s.Role != role || !samePlan(s.Plan, p) {
		return s, errors.New("saved migration does not match this host, plan, or role")
	}
	id, err := e.Runtime.HostID(ctx)
	if err != nil {
		return s, err
	}
	want := p.SourceHost
	if role == "destination" {
		want = p.DestinationHost
	}
	if id != want {
		return s, fmt.Errorf("migration host identity changed")
	}
	return s, nil
}

func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("migration state path is not a real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("migration directory must belong to the current user")
	}
	return os.Chmod(dir, 0700)
}
