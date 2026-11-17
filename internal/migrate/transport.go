package migrate

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (c Connection) sshArgs() []string {
	return []string{"-p", strconv.Itoa(c.Port), "-oBatchMode=yes", "-oStrictHostKeyChecking=yes", "-oConnectTimeout=10", "-oServerAliveInterval=5", "-oServerAliveCountMax=3", "-oForwardAgent=no", "-oClearAllForwardings=yes"}
}
func (c Connection) remoteCommand(args ...string) string {
	words := []string{c.Nomctl}
	if c.Sudo {
		words = []string{"sudo", "-n", c.Nomctl}
	}
	words = append(words, args...)
	for i, v := range words {
		words[i] = quote(v)
	}
	return strings.Join(words, " ")
}

type sshRemote struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Scanner
	done   chan error
	cancel context.CancelFunc
}

func Connect(ctx context.Context, c Connection) (Remote, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	args := append(c.sshArgs(), "--", c.Host, c.remoteCommand("migrate", "_serve"))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	r := &sshRemote{cmd: cmd, input: in, output: bufio.NewScanner(out), done: make(chan error, 1), cancel: cancel}
	r.output.Buffer(make([]byte, 4096), 2<<20)
	return r, nil
}

func (r *sshRemote) Call(ctx context.Context, req Request) (Response, error) {
	// A cancelled call terminates this session; stale responses are never reused.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			r.cancel()
		case <-done:
		}
	}()
	if err := json.NewEncoder(r.input).Encode(req); err != nil {
		return Response{}, errors.New("migration SSH control channel is unavailable; inspect status before resuming")
	}
	if !r.output.Scan() {
		return Response{}, errors.New("migration SSH control channel closed; both saved node states must be checked before resuming")
	}
	var response Response
	if err := json.Unmarshal(r.output.Bytes(), &response); err != nil {
		return response, errors.New("remote nomctl returned an invalid migration response")
	}
	if response.Protocol != Protocol {
		return response, errors.New("remote nomctl migration protocol is incompatible")
	}
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}
func (r *sshRemote) Close() error {
	_ = r.input.Close()
	go func() { r.done <- r.cmd.Wait() }()
	defer r.cancel()
	select {
	case err := <-r.done:
		return err
	case <-time.After(3 * time.Second):
		r.cancel()
		return <-r.done
	}
}

type Transfer func(context.Context, Plan, io.Writer) error

func Rsync(ctx context.Context, p Plan, progress io.Writer) error {
	if err := p.Validate(); err != nil {
		return err
	}
	ssh := append([]string{"ssh"}, p.Connection.sshArgs()...)
	for i, arg := range ssh {
		ssh[i] = quote(arg)
	}
	server := p.Connection.remoteCommand("migrate", "_receive", p.ID)
	common := append(rsyncFileArgs(p), "-e", strings.Join(ssh, " "), "--rsync-path="+server)
	for _, pair := range [][2]string{{p.Source.Binary, filepath.Join(p.Stage(), "node-binary")}, {p.Source.Data + "/", filepath.Join(p.Stage(), "data") + "/"}} {
		args := append([]string{}, common...)
		if strings.HasSuffix(pair[0], "/") {
			args = append(args, "--delete-delay")
		}
		args = append(args, "--", pair[0], rsyncHost(p.Connection.Host)+":"+pair[1])
		cmd := exec.CommandContext(ctx, "rsync", args...)
		cmd.Stdout = progress
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("rsync transfer interrupted or failed: %w; staging is retained for resume", err)
		}
	}
	return nil
}

// Rsync uses a colon to separate host and path, so IPv6 literals need brackets.
// SSH itself accepts the unbracketed host stored in the connection settings.
func rsyncHost(host string) string {
	user := ""
	if i := strings.LastIndexByte(host, '@'); i >= 0 {
		user, host = host[:i+1], host[i+1:]
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.Is6() {
		host = "[" + host + "]"
	}
	return user + host
}

func rsyncFileArgs(p Plan) []string {
	return []string{"-aH", "--checksum", "--modify-window=-1", "--fsync", "--sparse", "--numeric-ids", "--protect-args", "--partial", "--partial-dir=" + filepath.Join(p.Stage(), "partials"), "--info=progress2", "--out-format=", "--no-inc-recursive"}
}
