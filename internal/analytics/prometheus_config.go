package analytics

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// withNodeScrapeJob edits the scrape_configs sequence without changing other
// sections or duplicating an existing job. Unsupported layouts are left intact.
func withNodeScrapeJob(current []byte) ([]byte, bool, error) {
	var doc yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(current))
	if err := dec.Decode(&doc); err != nil {
		return nil, false, fmt.Errorf("parse Prometheus config: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, false, errors.New("expected exactly one Prometheus YAML document")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, false, errors.New("expected a Prometheus YAML mapping")
	}
	// Decode as well as parsing the node tree to reject duplicate mapping keys
	// and to recognize an existing job supplied through YAML aliases or merges.
	var resolved map[string]any
	if err := doc.Decode(&resolved); err != nil {
		return nil, false, fmt.Errorf("decode Prometheus config: %w", err)
	}
	if raw, ok := resolved["scrape_configs"]; ok && raw != nil {
		jobs, ok := raw.([]any)
		if !ok {
			return nil, false, errors.New("scrape_configs must be a sequence")
		}
		for _, rawJob := range jobs {
			job, ok := rawJob.(map[string]any)
			if !ok {
				return nil, false, errors.New("each scrape config must be a mapping")
			}
			if job["job_name"] == "node" {
				return current, false, nil
			}
		}
	}
	root := doc.Content[0]
	var jobs *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		if root.Content[i].Value == "scrape_configs" {
			jobs = root.Content[i+1]
			break
		}
	}
	switch {
	case jobs == nil:
		if _, inherited := resolved["scrape_configs"]; inherited {
			return nil, false, errors.New("edit inherited scrape_configs explicitly before installing analytics")
		}
		jobs = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "scrape_configs"}, jobs)
	case jobs.Kind == yaml.ScalarNode && jobs.Tag == "!!null":
		jobs.Kind, jobs.Tag, jobs.Value = yaml.SequenceNode, "!!seq", ""
	case jobs.Kind != yaml.SequenceNode || jobs.Anchor != "":
		return nil, false, errors.New("edit aliased scrape_configs explicitly before installing analytics")
	}
	var addition yaml.Node
	if err := yaml.Unmarshal([]byte("job_name: node\nstatic_configs:\n  - targets: ['127.0.0.1:9100']\n"), &addition); err != nil {
		return nil, false, err
	}
	jobs.Content = append(jobs.Content, addition.Content[0])
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, false, err
	}
	if err := enc.Close(); err != nil {
		return nil, false, err
	}
	return out.Bytes(), true, nil
}

// managedCandidate prepares a private sibling so validation happens before
// publishing the file. The caller removes the candidate on every unused path.
func managedCandidate(path string, data []byte, mode os.FileMode) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(f.Name())
		}
	}()
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	if err := f.Chmod(mode.Perm()); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return f.Name(), nil
}

// configurePrometheus validates a staged configuration, publishes it atomically,
// and restores the original bytes if activation fails. restoreService is only
// supplied when a previously running service should be recovered.
func configurePrometheus(path string, validate func(string) error, activate func(bool) error, restoreService func() error) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular config file", path)
	}
	current, err := readNoFollow(path)
	if err != nil {
		return err
	}
	next, changed, err := withNodeScrapeJob(current)
	if err != nil {
		return err
	}
	candidate, err := managedCandidate(path, next, info.Mode())
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(candidate) }()
	if err := validate(candidate); err != nil {
		return fmt.Errorf("validate Prometheus config: %w", err)
	}
	if changed {
		if err := os.Rename(candidate, path); err != nil {
			return err
		}
	}
	if err := activate(changed); err != nil {
		if !changed {
			return err
		}
		previous, restoreErr := managedCandidate(path, current, info.Mode())
		if restoreErr == nil {
			restoreErr = os.Rename(previous, path)
			_ = os.Remove(previous)
		}
		if restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore previous Prometheus config: %w", restoreErr))
		}
		if restoreService != nil {
			if restartErr := restoreService(); restartErr != nil {
				return errors.Join(err, fmt.Errorf("previous config restored, but service recovery failed: %w", restartErr))
			}
		}
		return fmt.Errorf("%w (previous Prometheus config restored)", err)
	}
	return nil
}
