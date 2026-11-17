package doctor

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/0x3639/nomctl/internal/nodeconfig"
)

type settings struct {
	rpcURL     string
	rpcEnabled bool
	port       int
	minPeers   int
	p2pHost    string
}

func readSettings(dir string) (settings, error) {
	data, err := readRegular(filepath.Join(dir, "config.json"), 1<<20)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return settings{}, err
	}
	d, err := nodeconfig.Parse(data)
	if err != nil {
		return settings{}, err
	}
	values := map[string]any{}
	for _, key := range []string{"RPC.EnableHTTP", "RPC.HTTPHost", "RPC.HTTPPort", "Net.ListenHost", "Net.ListenPort", "Net.MinPeers"} {
		schema, _ := nodeconfig.Lookup(key)
		value, present, err := d.Get(key)
		if err != nil {
			return settings{}, fmt.Errorf("invalid %s", key)
		}
		if !present {
			value = schema.Default
		}
		switch schema.Kind {
		case nodeconfig.Bool:
			if _, ok := value.(bool); !ok {
				return settings{}, fmt.Errorf("invalid %s", key)
			}
		case nodeconfig.String:
			host, ok := value.(string)
			if !ok || strings.TrimSpace(host) == "" {
				return settings{}, fmt.Errorf("%s must be a nonempty host", key)
			}
			// Only HTTP is contacted by this diagnostic. P2P hostnames are
			// supported by znnd; listener inspection needs no DNS lookup.
			if key == "RPC.HTTPHost" && host != "localhost" && net.ParseIP(host) == nil {
				return settings{}, fmt.Errorf("%s must be an IP address or localhost", key)
			}
		case nodeconfig.Int:
			n, ok := value.(int)
			if !ok {
				f, isNumber := value.(float64)
				if !isNumber || f < 0 || f > 65535 || f != float64(int(f)) {
					return settings{}, fmt.Errorf("invalid %s", key)
				}
				n = int(f)
			}
			if n < 0 || n > 65535 || (key != "Net.MinPeers" && n == 0) {
				return settings{}, fmt.Errorf("invalid %s", key)
			}
			value = n
		}
		values[key] = value
	}
	host := values["RPC.HTTPHost"].(string)
	if host == "0.0.0.0" || host == "localhost" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	s := settings{p2pHost: values["Net.ListenHost"].(string), rpcEnabled: values["RPC.EnableHTTP"].(bool), port: values["Net.ListenPort"].(int), minPeers: values["Net.MinPeers"].(int)}
	if s.rpcEnabled {
		s.rpcURL = "http://" + net.JoinHostPort(host, strconv.Itoa(values["RPC.HTTPPort"].(int)))
	}
	return s, nil
}

func readRegular(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds diagnostic size limit")
	}
	return data, nil
}
