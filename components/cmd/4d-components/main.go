// Command 4d-components resolves, acquires, verifies, caches and installs the component dependencies of a 4D
// project. It is an independent, stdlib-only implementation of the 4D dependency manager's observed behaviour
// (see docs/component-manager-behavior.md) and does not need Python, tool4d or GitHub Actions.
package main

import (
	"fmt"
	"os"
)

// Build information, set by the release build with -ldflags "-X main.buildVersion=...".
var buildVersion = "dev"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr interface {
	Write([]byte) (int, error)
}) int {
	if len(args) == 0 {
		usage(stderr)
		return exitUsage
	}
	switch args[0] {
	case "resolve":
		return runResolve(args[1:], stdout, stderr, true)
	case "install":
		return runResolve(args[1:], stdout, stderr, false)
	case "inventory":
		return runInventory(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "cache":
		return runCache(args[1:], stdout, stderr)
	case "version", "--version", "-version":
		fmt.Fprintf(stdout, "4d-components %s\n", buildVersion)
		return exitOK
	case "help", "--help", "-h":
		usage(stdout)
		return exitOK
	}
	fmt.Fprintf(stderr, "4d-components: unknown command %q\n", args[0])
	usage(stderr)
	return exitUsage
}

// Exit codes. 0: complete. 2: invalid invocation or input (nothing was resolved). 3: resolution ran but is
// incomplete (a dependency could not be resolved or installed). 1: unexpected internal error. The codes match
// the Python reference implementation so callers can switch implementations.
const (
	exitOK         = 0
	exitInternal   = 1
	exitUsage      = 2
	exitIncomplete = 3
)

func usage(w interface{ Write([]byte) (int, error) }) {
	fmt.Fprint(w, `usage: 4d-components <command> [options] [project]

commands:
  resolve    resolve the declared dependencies without installing anything (no archive is downloaded)
  install    resolve, download, verify and install dependencies into the 4D cache layout (and --components-dir)
  inventory  print the portable, deterministic dependency inventory (no absolute paths, no secrets)
  verify     check manifests, the last lock, stored archives and installed components without network access
  cache      list | verify | clean the verified archive store
  version    print the version

Run "4d-components <command> -h" for the options of a command.
Exit codes: 0 complete, 1 internal error, 2 invalid invocation or input, 3 incomplete resolution.
`)
}
