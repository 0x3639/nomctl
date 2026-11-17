package doctor

import (
	"fmt"
	"strings"
	"time"

	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/node"
)

func sampleChecks(a, b metrics.Sample, s settings, configErr error) []Check {
	var out []Check
	if configErr != nil {
		out = append(out, check("configuration", "error", "Cannot use node network settings", "config.json is unreadable or contains invalid HTTP/P2P settings.", "Run nomctl config show; check the data directory and HTTP/P2P settings."))
	}
	svc := b.Service
	switch {
	case svc.Error != "" && svc.ActiveState == "":
		out = append(out, check("service", "unknown", "Service state unavailable", "The systemd query did not provide a service state.", "Check systemctl status and collect nomctl support-bundle."))
	case !svc.Found || svc.ActiveState != "active":
		out = append(out, check("service", "error", "Node service is not active", fmt.Sprintf("%s: %s (%s); last result %s.", svc.Unit, known(svc.ActiveState), known(svc.SubState), known(svc.Result)), "Inspect nomctl logs and systemctl status before deciding whether to start or recover the node."))
	case svc.MainPID <= 0:
		out = append(out, check("service", "warning", "Service is active without a main process", fmt.Sprintf("%s: %s; no main PID reported.", svc.Unit, svc.SubState), "Inspect the unit type and ExecStart with systemctl status; active alone does not prove znnd is running."))
	default:
		out = append(out, check("service", "ok", "Node service is active", fmt.Sprintf("%s: %s; PID %d.", svc.Unit, svc.SubState, svc.MainPID), ""))
	}
	stable := a.Service.Error == "" && b.Service.Error == "" && a.Service.MainPID > 0 && a.Service.MainPID == b.Service.MainPID && a.Service.Since.Equal(b.Service.Since) && a.Service.NRestarts == b.Service.NRestarts
	switch {
	case a.Service.Error != "" || b.Service.Error != "":
		out = append(out, check("restarts", "unknown", "Restart comparison unavailable", "One service observation was unavailable.", "Repeat the check when systemd responds."))
	case b.Service.NRestarts > a.Service.NRestarts:
		out = append(out, check("restarts", "warning", "Node restarted during observation", fmt.Sprintf("Restart counter increased from %d to %d.", a.Service.NRestarts, b.Service.NRestarts), "Run nomctl support-bundle --watch to capture a recurring failure."))
	case !stable:
		out = append(out, check("restarts", "info", "No continuous running process observed", "The node was stopped, changed process, or reset its restart counter during this check.", "Repeat after maintenance or startup finishes."))
	default:
		out = append(out, check("restarts", "ok", "No restart observed", fmt.Sprintf("Restart counter remained %d during this observation window.", b.Service.NRestarts), ""))
	}
	n := b.Node
	switch {
	case configErr != nil:
		out = append(out, check("rpc", "unknown", "RPC endpoint could not be determined", "The configuration was not usable; no guessed endpoint was contacted.", "Fix the configuration issue above and repeat."))
	case !s.rpcEnabled:
		out = append(out, check("rpc", "info", "HTTP RPC is disabled in config.json", "RPC.EnableHTTP is false; RPC-based checks are unavailable.", "Review nomctl config show if HTTP RPC should be available locally."))
	case !n.Reachable:
		out = append(out, check("rpc", "error", "Node RPC checks did not all respond", s.rpcURL+" did not answer all required stats calls within the sample deadline.", "Inspect service logs, RPC settings and disk pressure; a busy node may need NOMCTL_RPC_TIMEOUT increased."))
	default:
		out = append(out, check("rpc", "ok", "Node RPC is responding", s.rpcURL+" answered the stats calls.", ""))
	}
	if !n.Reachable {
		return append(out,
			check("chain", "unknown", "Chain progress unavailable", "RPC measurements are incomplete.", "Resolve the RPC finding and repeat."),
			check("peers", "unknown", "Peer count unavailable", "RPC measurements are incomplete.", "Resolve the RPC finding and repeat."))
	}
	out = append(out, chainCheck(a, b, stable))
	peerEvidence := fmt.Sprintf("%d connected peers; configured dialing target Net.MinPeers=%d; node state %s.", n.NumPeers, s.minPeers, n.State.String())
	switch {
	case n.NumPeers < 0:
		out = append(out, check("peers", "unknown", "Invalid peer count", "RPC returned a negative peer count.", "Check the node version and RPC response."))
	case n.NumPeers == 0 || n.NumPeers < s.minPeers || n.State == node.NotEnoughPeers:
		out = append(out, check("peers", "warning", "Peer count needs attention", peerEvidence, fmt.Sprintf("Review the P2P results below, outbound connectivity, and firewall/NAT rules for the configured port %d/TCP.", s.port)))
	default:
		out = append(out, check("peers", "ok", "Peers are connected", peerEvidence, ""))
	}
	return out
}

func chainCheck(a, b metrics.Sample, stable bool) Check {
	n := b.Node
	evidence := fmt.Sprintf("Height %d → %d, target %d; %s; observations %.1fs apart.", a.Node.CurrentHeight, n.CurrentHeight, n.TargetHeight, n.State.String(), b.Taken.Sub(a.Taken).Seconds())
	switch {
	case !a.Node.Reachable || !stable:
		return check("chain", "unknown", "Chain progress comparison is incomplete", evidence, "Repeat after the node is continuously running and both RPC samples are available.")
	case n.CurrentHeight < a.Node.CurrentHeight:
		return check("chain", "warning", "Reported chain height decreased", evidence, "Check whether a restore or resync occurred; inspect logs before taking action.")
	case n.FrontierKnown && n.FrontierTime.After(b.Taken.Add(time.Minute)):
		return check("chain", "warning", "Frontier timestamp is ahead of the host clock", evidence, "Check clock synchronization; the timestamp difference alone cannot identify which clock is wrong.")
	case n.CurrentHeight > a.Node.CurrentHeight || (a.Node.FrontierKnown && n.FrontierKnown && n.FrontierHeight > a.Node.FrontierHeight):
		return check("chain", "ok", "Chain is making progress", evidence, "")
	case n.State == node.Done && n.FrontierKnown && b.Taken.Sub(n.FrontierTime) > metrics.StalledAfter:
		return check("chain", "error", "Synced node has a stale frontier and no observed progress", evidence+fmt.Sprintf(" Frontier age %s.", b.Taken.Sub(n.FrontierTime).Round(time.Second)), "Inspect logs and disk/peer/clock findings. Consider nomctl restart after checking for maintenance; collect a support bundle if this recurs.")
	case n.State == node.Done && n.FrontierKnown:
		return check("chain", "ok", "Synced frontier is recent", evidence+" A short unchanged height is normal between momentums.", "")
	case n.State == node.Unknown:
		return check("chain", "unknown", "Node reports an unknown sync state", evidence, "Inspect nomctl top and logs, then repeat.")
	case !n.FrontierKnown && n.State == node.Done:
		return check("chain", "unknown", "Frontier is unavailable", evidence+" The ledger query may be busy.", "Observe longer with nomctl top; a busy ledger alone does not establish a stall.")
	default:
		return check("chain", "warning", "No chain progress observed yet", evidence+" This short window does not prove a stall.", "Watch nomctl top for several minutes and inspect peers, logs and disk pressure if the height stays unchanged.")
	}
}

func diskCheck(id, path string, minimumKB int64, disk func(string) (int64, int, error)) Check {
	free, used, err := disk(path)
	if err != nil || free < 0 || used < 0 || used > 100 {
		return check(id, "unknown", "Disk space unavailable", path+": could not measure filesystem capacity.", "Check that the directory exists, is accessible, and its expected filesystem is mounted.")
	}
	evidence := fmt.Sprintf("%s: %.2f GiB available, %d%% used; configured minimum %.2f GiB.", path, float64(free)/(1024*1024), used, float64(minimumKB)/(1024*1024))
	if free < minimumKB || used >= 90 || free == 0 {
		return check(id, "warning", "Disk space is low", evidence, "Review storage use and recovery files, or expand the disk. Use the backup management menu for confirmed cleanup; never delete chain data as general cleanup.")
	}
	return check(id, "ok", "Disk space is available", evidence, "")
}

func clockCheck(synced bool, offset time.Duration, err error) Check {
	if err != nil {
		return check("clock", "unknown", "Clock synchronization unavailable", "Kernel time status could not be read.", "Inspect your time synchronization service (for example timedatectl or chronyc tracking).")
	}
	evidence := fmt.Sprintf("Kernel synchronized=%t; last offset correction %s (estimate, not an independent clock measurement).", synced, offset)
	if !synced || offset > time.Second || offset < -time.Second {
		return check("clock", "warning", "Clock synchronization needs attention", evidence, "Check your NTP service and its upstream time source. No clock settings were changed.")
	}
	return check("clock", "ok", "Kernel reports a synchronized clock", evidence, "")
}

var signatures = []struct {
	id, name, next string
	patterns       []string
}{
	{"log_fds", "Open-file limit reached", "Inspect the service LimitNOFILE setting and process open-file count with nomctl status.", []string{"too many open files"}},
	{"log_memory", "Memory exhaustion or forced kill", "Inspect memory pressure and the kernel journal; collect nomctl support-bundle before making changes.", []string{"out of memory", "oom-kill", "oom_kill", "status=9/kill"}},
	{"log_disk", "Filesystem allocation failed", "Check free space and inodes (df -h and df -i); review backup storage or grow the disk.", []string{"no space left on device"}},
	{"log_database", "Possible database damage", "Collect a support bundle and inspect the error. Review verified recovery options before any restore or resync.", []string{"corruption", "corrupted", "corrupt", "leveldb: error", "leveldb: manifest"}},
	{"log_crash", "Crash or fatal error", "Inspect nomctl logs and collect nomctl support-bundle --watch if failures repeat.", []string{"panic:", "fatal error:", "segmentation fault", "deadlock"}},
}

func logChecks(log string, err error) []Check {
	if err != nil {
		return []Check{check("logs", "unknown", "Recent journal could not be read", "The bounded journal query failed or timed out.", "Inspect journalctl permissions and collect nomctl support-bundle.")}
	}
	var out []Check
	lines := strings.Split(strings.ToLower(log), "\n")
	for _, signature := range signatures {
		count := 0
		for _, line := range lines {
			for _, pattern := range signature.patterns {
				if strings.Contains(line, pattern) {
					count++
					break
				}
			}
		}
		if count > 0 {
			out = append(out, check(signature.id, "warning", signature.name+" appeared in recent logs", fmt.Sprintf("%d matching lines in the sampled service journal (last hour, up to 1,000 lines/1 MiB). Historical evidence; this may already be resolved.", count), signature.next))
		}
	}
	if len(out) > 0 {
		return out
	}
	if strings.TrimSpace(log) == "" || strings.Contains(log, "[output truncated]") {
		return []Check{check("logs", "unknown", "Journal evidence is empty or truncated", "No known signature was found in the available output.", "Inspect nomctl logs or collect a support bundle for a wider window.")}
	}
	return []Check{check("logs", "info", "No known failure signatures in sampled journal", "Checked the last hour, limited to 1,000 lines/1 MiB. This does not rule out other failures or errors in application log files.", "")}
}
