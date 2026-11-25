package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/miyako/4d-static-analysis/components/internal/installer"
	"github.com/miyako/4d-static-analysis/components/internal/ojson"
)

func installationReport(res *installer.Result) *ojson.OMap {
	list := func(s []string) []any {
		out := make([]any, len(s))
		for i, v := range s {
			out[i] = v
		}
		return out
	}
	reuse := ojson.New()
	keys := make([]string, 0, len(res.Reuse))
	for k := range res.Reuse {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		reuse.Set(k, res.Reuse[k])
	}
	return ojson.New().Set("installed", list(res.Installed)).Set("providedByComponents", list(res.ProvidedByComponent)).
		Set("reuse", reuse).Set("problems", list(res.Problems))
}

func githubOutput(report *ojson.OMap, reportFile string) error {
	path := os.Getenv("GITHUB_OUTPUT")
	if path == "" {
		return nil
	}
	str := func(v any) string {
		if v == nil {
			return ""
		}
		return ojson.Str(v)
	}
	j := func(v any) string { b, _ := ojson.Marshal(v, ""); return string(b) }
	incomplete := report.Val("incomplete")
	complete := "true"
	if l, ok := incomplete.([]any); ok && len(l) > 0 {
		complete = "false"
	}
	values := [][2]string{{"dependencies-folder", str(report.Val("dependenciesFolder"))}, {"home", str(report.Val("home"))},
		{"lock-file", str(report.Val("lockFile"))}, {"resolved", j(report.Val("resolved"))}, {"incomplete", j(incomplete)},
		{"env", j(report.Val("env"))}, {"complete", complete}, {"report-file", reportFile}}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, kv := range values {
		b := make([]byte, 8)
		rand.Read(b)
		delim := "EOF_" + hex.EncodeToString(b)
		fmt.Fprintf(f, "%s<<%s\n%s\n%s\n", kv[0], delim, kv[1], delim)
	}
	return nil
}

func runResolve(args []string, stdout, stderr writer, planOnly bool) int {
	name := "install"
	if planOnly {
		name = "resolve"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f flags
	f.register(fs)
	if !planOnly {
		fs.StringVar(&f.componentsDir, "components-dir", "", "also install every resolved package as <dir>/<name>.4dbase (the analysis copy's Components folder); existing entries win")
		fs.StringVar(&f.workspace, "workspace", "", "analysis workspace: local components are installed only when they lie inside it")
	}
	if err := parseArgs(fs, args, &f); err != nil {
		fmt.Fprintf(stderr, "4d-components %s: %v\n", name, err)
		return exitUsage
	}
	if planOnly && f.rewrite != "" {
		fmt.Fprintln(stderr, "4d-components resolve: --rewrite changes files and belongs to install")
		return exitUsage
	}
	m, err := f.manager(planOnly)
	if err != nil {
		return errorExit(stderr, err)
	}
	report, err := m.Run()
	if err != nil {
		return errorExit(stderr, err)
	}
	var installation *installer.Result
	if !planOnly && f.componentsDir != "" {
		pkg := m.Package()
		if filepath.Clean(f.componentsDir) != filepath.Join(pkg, "Components") {
			fmt.Fprintln(stderr, "4d-components install: --components-dir must be the Components folder of the project package")
			return exitUsage
		}
		installation = installer.Install(report, pkg, f.workspace)
		report.Set("installation", installationReport(installation))
	}
	if err := emit(stdout, f.output, report); err != nil {
		return errorExit(stderr, err)
	}
	if err := githubOutput(report, f.output); err != nil {
		fmt.Fprintf(stderr, "4d-components: cannot write GITHUB_OUTPUT: %v\n", err)
		return exitInternal
	}
	prefix := ""
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		prefix = "::warning::"
	}
	incomplete, _ := report.Val("incomplete").([]any)
	if !f.quiet {
		for _, item := range incomplete {
			o := item.(*ojson.OMap)
			fmt.Fprintf(stderr, "%sdependency %s incomplete: %s\n", prefix, ojson.Str(o.Val("name")), ojson.Str(o.Val("reason")))
		}
		if installation != nil {
			for _, p := range installation.Problems {
				fmt.Fprintf(stderr, "%sinstallation: %s\n", prefix, p)
			}
		}
		warnings, _ := report.Val("warnings").([]any)
		for _, w := range warnings {
			fmt.Fprintf(stderr, "%s%s\n", prefix, ojson.Str(w))
		}
	}
	if len(incomplete) > 0 || (installation != nil && len(installation.Problems) > 0) {
		if f.allowIncomplete {
			return exitOK
		}
		return exitIncomplete
	}
	return exitOK
}
