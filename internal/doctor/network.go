package doctor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type listener struct{ address, inode string }

func parseListeners(data string, port int) ([]listener, error) {
	var out []listener
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] == "sl" {
			continue
		}
		if len(fields) < 10 {
			return nil, errors.New("malformed TCP table")
		}
		if fields[3] != "0A" {
			continue
		}
		host, p, ok := strings.Cut(fields[1], ":")
		parsedPort, err := strconv.ParseUint(p, 16, 16)
		if !ok || err != nil {
			return nil, errors.New("malformed listener port")
		}
		if int(parsedPort) != port {
			continue
		}
		ip, err := hex.DecodeString(host)
		if err != nil || (len(ip) != 4 && len(ip) != 16) {
			return nil, errors.New("malformed listener address")
		}
		// Linux exports native-endian 32-bit words; supported release targets
		// (linux amd64/arm64) are little endian.
		for i := 0; i < len(ip); i += 4 {
			ip[i], ip[i+1], ip[i+2], ip[i+3] = ip[i+3], ip[i+2], ip[i+1], ip[i]
		}
		out = append(out, listener{net.IP(ip).String(), fields[9]})
	}
	return out, nil
}

func localNetworkCheck(ctx context.Context, proc string, pid, port int) Check {
	unknown := func(why string) Check {
		return check("p2p_local", "unknown", "Local P2P listener evidence incomplete", why, "Inspect ss -ltnp on the node and compare its listening port with nomctl config show.")
	}
	var found []listener
	for _, name := range []string{"tcp", "tcp6"} {
		data, err := readRegular(filepath.Join(proc, "net", name), 4<<20)
		if name == "tcp6" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return unknown("Linux TCP listener tables could not be read.")
		}
		entries, err := parseListeners(string(data), port)
		if err != nil {
			return unknown("Linux TCP listener tables could not be parsed.")
		}
		found = append(found, entries...)
	}
	if len(found) == 0 {
		return check("p2p_local", "warning", "No local TCP listener on the configured P2P port", fmt.Sprintf("No listening socket on port %d/TCP was observed.", port), "Check the service state and Net.ListenHost/Net.ListenPort. This does not establish an external firewall result.")
	}
	addresses := make([]string, 0, len(found))
	for _, l := range found {
		addresses = append(addresses, net.JoinHostPort(l.address, strconv.Itoa(port)))
	}
	sort.Strings(addresses)
	evidence := "Observed TCP listeners: " + strings.Join(addresses, ", ") + ". "
	if pid <= 0 {
		return unknown(evidence + "No node PID is available to confirm ownership.")
	}
	dir := filepath.Join(proc, strconv.Itoa(pid), "fd")
	directory, err := os.Open(dir)
	if err != nil {
		return unknown(evidence + "Node socket ownership could not be inspected.")
	}
	defer directory.Close()
	fds, err := directory.ReadDir(65537)
	if (err != nil && !errors.Is(err, io.EOF)) || len(fds) > 65536 {
		return unknown(evidence + "Node socket ownership exceeded diagnostic limits.")
	}
	owned := map[string]bool{}
	complete := true
	for _, fd := range fds {
		if ctx.Err() != nil {
			return unknown("Listener inspection was cancelled.")
		}
		target, err := os.Readlink(filepath.Join(dir, fd.Name()))
		if err != nil {
			complete = false
			continue
		}
		if strings.HasPrefix(target, "socket:[") {
			owned[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}
	nodeListener, outside := false, false
	for _, l := range found {
		if owned[l.inode] {
			nodeListener = true
			ip := net.ParseIP(l.address)
			if !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				outside = true
			}
		}
	}
	if !nodeListener {
		return unknown(evidence + "No listener was confirmed as owned by the observed node PID; another process may own this port.")
	}
	if !complete {
		return unknown(evidence + "Socket ownership inspection was incomplete; repeat the check.")
	}
	if !outside {
		return check("p2p_local", "warning", "Node P2P listener is limited to local addresses", evidence, "Review Net.ListenHost if peers should connect from other machines; keep RPC access private.")
	}
	return check("p2p_local", "ok", "Node owns a local P2P listener", evidence+"A listening socket does not prove outside reachability.", "")
}

func validateProbe(endpoint, address string) error {
	if endpoint == "" && address == "" {
		return nil
	}
	if endpoint == "" || address == "" {
		return errors.New("--probe-url and --public-ip must be supplied together")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("--probe-url must be an HTTPS URL without credentials, query, or fragment")
	}
	ip := net.ParseIP(address)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return errors.New("--public-ip must be a public IP literal for this node")
	}
	return nil
}

func externalCheck(ctx context.Context, opts Options, port int) Check {
	unknown := func(why string) Check {
		return check("p2p_external", "unknown", "Outside P2P reachability is unknown", why, "Check firewall/NAT and test the configured P2P port from another machine, or supply a compatible --probe-url and --public-ip.")
	}
	if opts.ProbeURL == "" {
		return unknown("No external probe requested; no external endpoint was contacted.")
	}
	if port <= 0 {
		return unknown("Configured P2P port is unavailable; no external endpoint was contacted.")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return probeWithClient(ctx, opts, port, client, unknown)
}

func probeWithClient(ctx context.Context, opts Options, port int, client *http.Client, unknown func(string) Check) Check {
	u, err := url.Parse(opts.ProbeURL)
	if err != nil {
		return unknown("External probe URL is invalid.")
	}
	query := u.Query()
	query.Set("address", opts.PublicIP)
	query.Set("port", strconv.Itoa(port))
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return unknown("External probe request could not be created.")
	}
	resp, err := client.Do(req)
	if err != nil {
		return unknown("External probe did not respond; this does not establish a closed port.")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return unknown(fmt.Sprintf("External probe returned HTTP %d; this does not establish a closed port.", resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(data) > 4096 {
		return unknown("External probe response was unreadable or too large.")
	}
	var result struct {
		Address   string `json:"address"`
		Port      int    `json:"port"`
		Reachable *bool  `json:"reachable"`
	}
	if json.Unmarshal(data, &result) != nil || result.Reachable == nil || result.Port != port || !net.ParseIP(result.Address).Equal(net.ParseIP(opts.PublicIP)) {
		return unknown("External probe response did not contain a valid result for the requested target.")
	}
	evidence := fmt.Sprintf("%s reported reachable=%t for %s. This is the selected probe's observation, not a guarantee for all peers.", opts.ProbeURL, *result.Reachable, net.JoinHostPort(opts.PublicIP, strconv.Itoa(port)))
	if !*result.Reachable {
		return check("p2p_external", "warning", "Selected probe could not reach the P2P port", evidence, "Verify the public IP and firewall/NAT rules, then test from another network.")
	}
	return check("p2p_external", "ok", "Selected probe reached the P2P port", evidence, "")
}
