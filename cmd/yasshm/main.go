// Command yasshm is a self-hosted MCP gateway that runs policy-checked,
// audited commands on SSH hosts its operator has configured.
package main

import (
	"os"

	"github.com/InfiniteRoomLabs/yasshm/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
