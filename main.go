// nomctl is a single-binary CLI/TUI for deploying and operating Zenon Network
// (NoM) nodes. It is a Go port of github.com/hypercore-one/deployment.
package main

import (
	"os"

	"github.com/0x3639/nomctl/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
