package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
)

const helpText = `*nomctl alerts bot*

/start \- get a pairing code for a new node
/nodes \- list your paired nodes
/mute NAME ALERT \[DURATION\] \- silence an alert \(or "all"\), default 24h
/unmute NAME ALERT \- resume an alert \(or "all"\)
/unpair NAME \- forget a node
/help \- this text

Pair a node with: ` + "`sudo nomctl alerts setup`"

// HandleUpdate processes one Telegram message.
func (s *Server) HandleUpdate(ctx context.Context, u Update) error {
	if u.ChatID == 0 || !strings.HasPrefix(u.Text, "/") {
		return nil
	}
	// An incoming message proves the chat can reach the bot; lift any block.
	s.clearBlocked(ctx, u.ChatID)
	fields := strings.Fields(u.Text)
	cmd := strings.ToLower(fields[0])
	if i := strings.IndexByte(cmd, '@'); i > 0 { // /start@botname
		cmd = cmd[:i]
	}
	args := fields[1:]
	var reply string
	var err error
	switch cmd {
	case "/start":
		reply, err = s.cmdStart(ctx, u.ChatID)
	case "/nodes":
		reply, err = s.cmdNodes(ctx, u.ChatID)
	case "/mute":
		reply, err = s.cmdMute(ctx, u.ChatID, args)
	case "/unmute":
		reply, err = s.cmdUnmute(ctx, u.ChatID, args)
	case "/unpair":
		reply, err = s.cmdUnpair(ctx, u.ChatID, args)
	default:
		reply = helpText
	}
	if err != nil {
		slog.Error("command failed", "cmd", cmd, "chat", u.ChatID, "err", err)
		reply = Escape("Something went wrong; please try again.")
	}
	return s.tg.Send(ctx, u.ChatID, reply)
}

func (s *Server) cmdStart(ctx context.Context, chatID int64) (string, error) {
	code := NewCode()
	now := s.opts.Now()
	if err := s.store.CreateCode(ctx, chatID, code, now.Add(CodeTTL)); err != nil {
		if errors.Is(err, ErrTooManyCodes) {
			return Escape("You already have several unused pairing codes. Use one of them, or wait 10 minutes."), nil
		}
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Your pairing code is %s \\(valid %d minutes\\)\\.\n\nOn the node run:\n%s\n\nand enter the code when asked\\.",
		Code(code), int(CodeTTL.Minutes()), Code("sudo nomctl alerts setup --code "+code))
	if s.opts.PublicURL != "" {
		fmt.Fprintf(&b, "\n\nRelay: %s", Escape(s.opts.PublicURL))
	}
	return b.String(), nil
}

func (s *Server) cmdNodes(ctx context.Context, chatID int64) (string, error) {
	nodes, err := s.store.ListNodes(ctx, chatID)
	if err != nil {
		return "", err
	}
	if len(nodes) == 0 {
		return Escape("No nodes paired with this chat yet. Send /start to get a pairing code."), nil
	}
	now := s.opts.Now()
	var b strings.Builder
	for _, n := range nodes {
		age := now.Sub(n.LastSeen).Round(time.Second)
		status := "🟢"
		if n.Silent {
			status = "🔴 silent"
		}
		fmt.Fprintf(&b, "%s %s \\(%s\\)\nlast seen %s ago", status, Bold(n.Name), Escape(n.Host), Escape(age.String()))
		if n.Summary.State != "" {
			fmt.Fprintf(&b, "\n%s", Escape(fmt.Sprintf("%s · height %d · %d peers · %d restarts", n.Summary.State, n.Summary.Height, n.Summary.Peers, n.Summary.Restarts)))
		}
		b.WriteString("\n\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (s *Server) findNode(ctx context.Context, chatID int64, name string) (Node, bool, error) {
	nodes, err := s.store.ListNodes(ctx, chatID)
	if err != nil {
		return Node{}, false, err
	}
	for _, n := range nodes {
		if strings.EqualFold(n.Name, name) {
			return n, true, nil
		}
	}
	return Node{}, false, nil
}

func validAlert(name string) bool {
	if name == "all" {
		return true
	}
	_, ok := alertproto.Lookup(name)
	return ok
}

// parseMuteDuration accepts Go durations plus a "d" suffix for days.
func parseMuteDuration(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("bad duration %q", s)
	}
	return d, nil
}

func (s *Server) cmdMute(ctx context.Context, chatID int64, args []string) (string, error) {
	if len(args) < 2 {
		return Escape("Usage: /mute NAME ALERT [DURATION], e.g. /mute pillar-1 disk_low 2d"), nil
	}
	node, ok, err := s.findNode(ctx, chatID, args[0])
	if err != nil {
		return "", err
	}
	if !ok {
		return Escape(fmt.Sprintf("No node named %q in this chat. See /nodes.", args[0])), nil
	}
	alert := strings.ToLower(args[1])
	if !validAlert(alert) {
		return Escape(fmt.Sprintf("Unknown alert %q. Use one of: %s, or all.", args[1], strings.Join(alertNames(), ", "))), nil
	}
	d := 24 * time.Hour
	if len(args) >= 3 {
		d, err = parseMuteDuration(args[2])
		if err != nil {
			return Escape("Duration must look like 30m, 2h, 1d."), nil
		}
	}
	if err := s.store.SetMute(ctx, Mute{NodeID: node.ID, Alert: alert, Until: s.opts.Now().Add(d)}); err != nil {
		return "", err
	}
	return Escape(fmt.Sprintf("Muted %s on %s for %s.", alert, node.Name, d)), nil
}

func (s *Server) cmdUnmute(ctx context.Context, chatID int64, args []string) (string, error) {
	if len(args) < 2 {
		return Escape("Usage: /unmute NAME ALERT"), nil
	}
	node, ok, err := s.findNode(ctx, chatID, args[0])
	if err != nil {
		return "", err
	}
	if !ok {
		return Escape(fmt.Sprintf("No node named %q in this chat. See /nodes.", args[0])), nil
	}
	alert := strings.ToLower(args[1])
	if !validAlert(alert) {
		return Escape(fmt.Sprintf("Unknown alert %q. Use one of: %s, or all.", args[1], strings.Join(alertNames(), ", "))), nil
	}
	if err := s.store.ClearMute(ctx, node.ID, alert); err != nil {
		return "", err
	}
	return Escape(fmt.Sprintf("Unmuted %s on %s.", alert, node.Name)), nil
}

func (s *Server) cmdUnpair(ctx context.Context, chatID int64, args []string) (string, error) {
	if len(args) < 1 {
		return Escape("Usage: /unpair NAME"), nil
	}
	node, ok, err := s.findNode(ctx, chatID, args[0])
	if err != nil {
		return "", err
	}
	if !ok {
		return Escape(fmt.Sprintf("No node named %q in this chat. See /nodes.", args[0])), nil
	}
	if err := s.store.DeleteNode(ctx, node.ID); err != nil {
		return "", err
	}
	slog.Info("node unpaired by chat", "node", node.ID, "name", node.Name)
	return Escape(fmt.Sprintf("Unpaired %s. Its alerts daemon will stop reporting; run `sudo nomctl alerts setup` on the node to pair again.", node.Name)), nil
}

func alertNames() []string {
	var names []string
	for _, a := range alertproto.Alerts {
		names = append(names, a.Name)
	}
	return names
}
