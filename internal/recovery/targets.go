// Package recovery verifies stored recovery files without touching live data.
package recovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0x3639/nomctl/internal/backup"
	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/restore"
)

type Target struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Service string `json:"service"`
}

func Backup(cfg config.Config, file string) Target {
	path, _ := filepath.Abs(restore.Resolve(cfg, file))
	return Target{Kind: "backup", Path: path, Service: cfg.ServiceName}
}

func Point(cfg config.Config, id string) (Target, error) {
	if id == "" || filepath.Base(id) != id || !strings.HasPrefix(id, cfg.ServiceName+"_point_") {
		return Target{}, errors.New("invalid restore point ID")
	}
	path, err := filepath.Abs(filepath.Join(backup.RestorePointsDir(cfg), id))
	return Target{Kind: "restore-point", Path: path, Service: cfg.ServiceName}, err
}

// List deliberately includes incomplete artifacts excluded by backup.List.
func List(cfg config.Config) ([]Target, error) {
	var targets []Target
	seen := map[Target]bool{}
	for _, kind := range []string{"backup", "restore-point"} {
		dir := cfg.BackupDir
		if kind == "restore-point" {
			dir = backup.RestorePointsDir(cfg)
		}
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if kind == "backup" && strings.HasPrefix(e.Name(), cfg.ServiceName+"_backup_") && (strings.HasSuffix(e.Name(), ".tar.gz") || strings.HasSuffix(e.Name(), ".hash")) {
				name := e.Name()
				if strings.HasSuffix(name, ".hash") {
					name = strings.TrimSuffix(name, ".hash") + ".tar.gz"
				}
				t := Backup(cfg, name)
				if !seen[t] {
					targets = append(targets, t)
					seen[t] = true
				}
			}
			if kind == "restore-point" && strings.HasPrefix(e.Name(), cfg.ServiceName+"_point_") {
				t, err := Point(cfg, e.Name())
				if err != nil {
					return nil, err
				}
				targets = append(targets, t)
			}
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Path < targets[j].Path })
	return targets, nil
}

func (t Target) paths() ([]string, error) {
	if !filepath.IsAbs(t.Path) {
		return nil, errors.New("recovery path must be absolute")
	}
	switch t.Kind {
	case "backup":
		return []string{t.Path, backup.HashPath(t.Path)}, nil
	case "restore-point":
		return []string{filepath.Join(t.Path, "chain.tar.gz"), filepath.Join(t.Path, "chain.hash"), filepath.Join(t.Path, "point.json"), filepath.Join(t.Path, "node-binary")}, nil
	default:
		return nil, fmt.Errorf("unknown recovery file type %q", t.Kind)
	}
}

func (t Target) parents() error {
	if t.Kind != "restore-point" {
		return nil
	}
	for _, path := range []string{filepath.Dir(t.Path), t.Path} {
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !st.IsDir() {
			return fmt.Errorf("restore point requires a real directory: %s", path)
		}
	}
	return nil
}
