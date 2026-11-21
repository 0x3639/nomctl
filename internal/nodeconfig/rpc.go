package nodeconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// ReadLocalHTTPURL reads only the node's HTTP connection settings. A missing
// config or key uses znnd's defaults; disabled HTTP returns an empty URL.
// Invalid or unreadable configuration must not fall back to another listener.
func ReadLocalHTTPURL(dir string) (string, error) {
	f, err := os.OpenFile(filepath.Join(dir, "config.json"), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	var data []byte
	if err == nil {
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return "", err
		}
		if !st.Mode().IsRegular() {
			return "", errors.New("config.json is not a regular file")
		}
		const limit = 1 << 20
		data, err = io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil {
			return "", err
		}
		if len(data) > limit {
			return "", errors.New("config.json exceeds the RPC settings size limit")
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return "", errors.New("config.json is empty")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	d, err := Parse(data)
	if err != nil {
		return "", err
	}
	return d.LocalHTTPURL()
}

// LocalHTTPURL resolves a bind address to a usable local HTTP endpoint.
// Wildcard listeners are contacted through loopback, never through a public
// proxy. RPC settings are validated independently of unrelated node settings.
func (d *Document) LocalHTTPURL() (string, error) {
	values := make(map[string]any, 3)
	for _, key := range []string{"RPC.EnableHTTP", "RPC.HTTPHost", "RPC.HTTPPort"} {
		schema, _ := Lookup(key)
		value, present, err := d.Get(key)
		if err != nil {
			return "", fmt.Errorf("invalid %s", key)
		}
		if !present {
			value = schema.Default
		}
		values[key] = value
	}
	enabled, ok := values["RPC.EnableHTTP"].(bool)
	if !ok {
		return "", errors.New("invalid RPC.EnableHTTP")
	}
	host, ok := values["RPC.HTTPHost"].(string)
	if !ok || (host != "localhost" && net.ParseIP(host) == nil) {
		return "", errors.New("RPC.HTTPHost must be an IP address or localhost")
	}
	port, ok := values["RPC.HTTPPort"].(int)
	if !ok {
		f, isNumber := values["RPC.HTTPPort"].(float64)
		if !isNumber || f < 1 || f > 65535 || f != float64(int(f)) {
			return "", errors.New("invalid RPC.HTTPPort")
		}
		port = int(f)
	}
	if port < 1 || port > 65535 {
		return "", errors.New("invalid RPC.HTTPPort")
	}
	if !enabled {
		return "", nil
	}
	switch host {
	case "0.0.0.0", "localhost":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
}
