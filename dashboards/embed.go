// Package dashboards embeds the Grafana dashboard definitions shipped with
// nomctl so the binary needs no files on disk.
package dashboards

import "embed"

// FS contains every *.json dashboard in this directory.
//
//go:embed *.json
var FS embed.FS

// Node is the file name of the znnd node dashboard.
const Node = "znnd.json"
