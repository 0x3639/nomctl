package relay

import (
	"fmt"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/alertproto"
)

// NodeCard renders one node for /nodes: a header line, then one labelled
// line per topic. Lines whose data the node did not send are left out, so
// nodes on older releases still render.
func NodeCard(n Node, now time.Time) string {
	var b strings.Builder
	age := now.Sub(n.LastSeen).Round(time.Second)
	head := "🟢 " + Bold(n.Name)
	if n.Silent {
		head = "🔴 " + Bold(n.Name) + " · silent"
	}
	if n.Host != "" && n.Host != n.Name {
		head += " · " + Escape(n.Host)
	}
	head += " · seen " + Escape(humanAge(age)) + " ago"
	b.WriteString(head)

	s := n.Summary
	if s.State == "" {
		return b.String()
	}
	line := func(label, text string) {
		if text != "" {
			b.WriteString("\n" + Bold(label) + " " + Escape(text))
		}
	}
	line("Sync", syncText(s))
	switch {
	case s.LedgerBusy:
		line("Frontier", "ledger busy (inserting momentums)")
	case s.Frontier > 0:
		line("Frontier", fmt.Sprintf("%s · %s ago", commas(s.Frontier), humanAge(time.Duration(s.FrontierAgeSeconds)*time.Second)))
	}
	peers := fmt.Sprintf("%d · restarts %d", s.Peers, s.Restarts)
	if s.UptimeSeconds > 0 {
		peers += " · up " + humanAge(time.Duration(s.UptimeSeconds)*time.Second)
	}
	line("Peers", peers)
	if s.PillarName != "" {
		switch {
		case s.PillarError != "":
			line("Pillar", s.PillarName+": "+s.PillarError)
		default:
			line("Pillar", fmt.Sprintf("%s rank %d · %d / %d produced this epoch", s.PillarName, s.PillarRank, s.PillarProduced, s.PillarExpected))
		}
	}
	var nodeParts []string
	if s.NodeVersion != "" {
		v := "znnd " + s.NodeVersion
		if s.NodeCommit != "" {
			v += " (" + s.NodeCommit + ")"
		}
		nodeParts = append(nodeParts, v)
	}
	if s.NomctlVersion != "" {
		nodeParts = append(nodeParts, "nomctl "+s.NomctlVersion)
	}
	line("Node", strings.Join(nodeParts, " · "))
	var host []string
	if s.MemTotal > 0 {
		host = append(host, fmt.Sprintf("load %.1f", s.Load1), fmt.Sprintf("mem %s / %s free", gb(s.MemFree), gb(s.MemTotal)))
	}
	if s.DiskTotal > 0 {
		host = append(host, fmt.Sprintf("disk %s / %s free (%d%%)", gb(s.DiskFree), gb(s.DiskTotal), s.DiskFree*100/s.DiskTotal))
	}
	line("Host", strings.Join(host, " · "))
	if s.RSS > 0 {
		line("Process", fmt.Sprintf("cpu %.0f%% · rss %s", s.CPUPercent, gb(s.RSS)))
	}
	var upd []string
	if s.NomctlUpdate != "" {
		upd = append(upd, "nomctl "+strings.TrimPrefix(s.NomctlUpdate, "v")+" available")
	}
	if s.NodeUpdate {
		upd = append(upd, "go-zenon has new commits")
	}
	line("Update", strings.Join(upd, " · "))
	return b.String()
}

func syncText(s alertproto.Summary) string {
	text := s.State
	if s.Height > 0 {
		text += " " + commas(s.Height)
		if s.TargetHeight > 0 {
			text += fmt.Sprintf(" / %s (%.1f%%)", commas(s.TargetHeight), float64(s.Height)/float64(s.TargetHeight)*100)
		}
	}
	if s.MomentumRate > 0 {
		text += fmt.Sprintf(" · %.1f mom/s", s.MomentumRate)
	}
	if s.ETASeconds > 0 {
		text += " · ETA " + humanAge(time.Duration(s.ETASeconds)*time.Second)
	}
	return text
}

func gb(b uint64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.1f TB", float64(b)/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	default:
		return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
	}
}

func commas(n uint64) string {
	s := fmt.Sprint(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func humanAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	case mins > 0:
		return fmt.Sprintf("%dm %ds", mins, secs)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}
