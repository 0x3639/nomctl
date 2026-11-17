package doctor

import (
	"context"
	"fmt"
	"time"

	"github.com/0x3639/nomctl/internal/config"
	"github.com/0x3639/nomctl/internal/metrics"
	"github.com/0x3639/nomctl/internal/node"
)

// LocalSettings contains only connection settings, never producer secrets.
type LocalSettings struct {
	RPCURL   string `json:"rpc_url"`
	P2PHost  string `json:"p2p_host"`
	P2PPort  int    `json:"p2p_port"`
	MinPeers int    `json:"min_peers"`
}

func ReadLocalSettings(dir string) (LocalSettings, error) {
	s, err := readSettings(dir)
	if err != nil {
		return LocalSettings{}, err
	}
	if !s.rpcEnabled {
		return LocalSettings{}, fmt.Errorf("HTTP RPC is disabled; health checks require local RPC")
	}
	return LocalSettings{RPCURL: s.rpcURL, P2PHost: s.p2pHost, P2PPort: s.port, MinPeers: s.minPeers}, nil
}

type NodeObservation struct {
	Sample   metrics.Sample `json:"sample"`
	Identity Identity       `json:"identity"`
	Problem  string         `json:"problem,omitempty"`
}

// ObserveNode brackets local RPC and executable identity with service queries.
func ObserveNode(ctx context.Context, cfg config.Config) NodeObservation {
	ctx, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	s, err := ReadLocalSettings(cfg.ZnnDir)
	if err != nil {
		return NodeObservation{Problem: err.Error()}
	}
	sampler := metrics.NewSampler(cfg)
	sampler.Node = node.NewWithTimeout(s.RPCURL, cfg.RPCTimeout)
	sampler.SetPillarName("")
	o := observe(ctx, sampler.Take, metrics.ReadServicePropsContext)
	r := NodeObservation{Sample: o.Sample, Problem: o.continuity}
	r.Identity = collectIdentity(ctx, cfg.BinaryPath(), "/proc", o.Service, o.Node)
	return r
}
