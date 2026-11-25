package main

import (
	"flag"
	"fmt"

	"github.com/miyako/4d-static-analysis/components/internal/inventory"
	"github.com/miyako/4d-static-analysis/components/internal/ojson"
)

// runInventory prints the portable inventory. It resolves without installing unless --installed is given; it
// never writes the lock file.
func runInventory(args []string, stdout, stderr writer) int {
	fs := flag.NewFlagSet("inventory", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f flags
	f.register(fs)
	installed := false
	fs.BoolVar(&installed, "installed", false, "download and verify the archives (like install) so that the inventory records their SHA-256")
	if err := parseArgs(fs, args, &f); err != nil {
		fmt.Fprintf(stderr, "4d-components inventory: %v\n", err)
		return exitUsage
	}
	f.noWriteLock = true
	m, err := f.manager(!installed)
	if err != nil {
		return errorExit(stderr, err)
	}
	report, err := m.Run()
	if err != nil {
		return errorExit(stderr, err)
	}
	inv := inventory.Build(report, buildVersion)
	if err := emit(stdout, f.output, inv); err != nil {
		return errorExit(stderr, err)
	}
	if inv.Val("complete") == false && !f.allowIncomplete {
		return exitIncomplete
	}
	return exitOK
}

var _ = ojson.New
