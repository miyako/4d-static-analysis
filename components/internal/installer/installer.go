// Package installer places resolved components into an analysis workspace. It never touches the original
// project: callers pass a disposable copy. A pre-existing Components/ entry always takes precedence, as in 4D.
package installer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/miyako/4d-static-analysis/components/internal/fsx"
	"github.com/miyako/4d-static-analysis/components/internal/ojson"
)

// Result summarises an installation.
type Result struct {
	Installed           []string
	ProvidedByComponent []string
	Reuse               map[string]string
	Problems            []string
}

func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func realpath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	abs, _ := filepath.Abs(p)
	return abs
}

// localSource returns the folder to copy for a local component, or the reason it is not installed. Only local
// components inside the analysis workspace are installed; anything else stays incomplete.
func localSource(entry *ojson.OMap, pkg, workspace string) (string, string) {
	declared := "with no path"
	if ojson.Truthy(entry.Val("local")) {
		declared = ojson.Repr(entry.Val("local"))
	}
	if workspace == "" {
		return "", "local dependencies are not installed by this run"
	}
	if !ojson.Truthy(entry.Val("path")) {
		return "", fmt.Sprintf("local component %s was not found or not resolved", declared)
	}
	folder := realpath(ojson.StrOrEmpty(entry.Val("path")))
	if !within(folder, realpath(workspace)) {
		return "", fmt.Sprintf("local component %s is outside the analyzed repository; only local components inside it are installed", declared)
	}
	if st, err := os.Stat(folder); err != nil || !st.IsDir() {
		return "", fmt.Sprintf("local component %s was not found", declared)
	}
	if within(realpath(pkg), folder) {
		return "", fmt.Sprintf("local component %s contains the analyzed project", declared)
	}
	return folder, ""
}

// copyStaged copies src to target through a staging folder, so a failed copy never leaves a package that could
// be mistaken for a complete installation.
func copyStaged(src, target string) error {
	stage := filepath.Join(filepath.Dir(target), fmt.Sprintf(".%s.%s.partial", filepath.Base(target), fsx.Unique()))
	os.RemoveAll(stage)
	if err := fsx.CopyTree(src, stage); err != nil {
		os.RemoveAll(stage)
		return err
	}
	if err := os.Rename(stage, target); err != nil {
		os.RemoveAll(stage)
		return err
	}
	return nil
}

// Install copies each resolved package of the report into `<pkg>/Components/<name>.4dbase`.
func Install(report *ojson.OMap, pkg, workspace string) *Result {
	res := &Result{Reuse: map[string]string{}}
	problem := func(name, msg string) { res.Problems = append(res.Problems, name+": "+msg) }
	if inc, ok := report.Val("incomplete").([]any); ok {
		for _, item := range inc {
			if o, ok := item.(*ojson.OMap); ok {
				res.Problems = append(res.Problems, ojson.Str(o.Val("name"))+": "+ojson.Str(o.Val("reason")))
			}
		}
	}
	deps, _ := report.Val("dependencies").(*ojson.OMap)
	if deps == nil {
		return res
	}
	components := filepath.Join(pkg, "Components")
	names := deps.Keys()
	sort.Strings(names)
	for _, name := range names {
		entry, _ := deps.Val(name).(*ojson.OMap)
		if entry == nil {
			continue
		}
		if ojson.Truthy(entry.Val("overloadedBy")) {
			res.ProvidedByComponent = append(res.ProvidedByComponent, name)
			continue
		}
		if entry.Val("source") == "local" {
			if ojson.Truthy(entry.Val("error")) {
				continue // already reported (unreadable manifest, version conflict)
			}
			src, reason := localSource(entry, pkg, workspace)
			if reason != "" {
				problem(name, reason)
				continue
			}
			target := filepath.Join(components, name+".4dbase")
			if _, err := os.Lstat(target); err == nil {
				res.ProvidedByComponent = append(res.ProvidedByComponent, name)
				continue
			}
			if err := os.MkdirAll(components, 0o755); err != nil {
				problem(name, err.Error())
				continue
			}
			if err := copyStaged(src, target); err != nil {
				problem(name, "not installed: "+err.Error())
				continue
			}
			res.Installed = append(res.Installed, name)
			res.Reuse[name] = "local"
			continue
		}
		src := ojson.StrOrEmpty(entry.Val("path"))
		if src == "" || !isDir(src) {
			reported := false
			for _, p := range res.Problems {
				if strings.HasPrefix(p, name+":") {
					reported = true
				}
			}
			if !reported {
				problem(name, "not installed")
			}
			continue
		}
		target := filepath.Join(components, name+".4dbase")
		if err := os.MkdirAll(components, 0o755); err != nil {
			problem(name, err.Error())
			continue
		}
		if err := copyStaged(src, target); err != nil {
			problem(name, "not installed: "+err.Error())
			continue
		}
		res.Installed = append(res.Installed, name)
		res.Reuse[name] = ojson.StrOrEmpty(entry.Val("reuse"))
	}
	return res
}

func isDir(p string) bool { st, err := os.Stat(p); return err == nil && st.IsDir() }

// Verify checks that every installed component of a workspace is a loadable package.
func Verify(pkg string, names []string) []string {
	var problems []string
	for _, n := range names {
		root := filepath.Join(pkg, "Components", n+".4dbase")
		if fsx.PackageForm(root) == "" {
			problems = append(problems, fmt.Sprintf("%s: Components/%s.4dbase is missing or not a package", n, n))
		}
	}
	return problems
}
