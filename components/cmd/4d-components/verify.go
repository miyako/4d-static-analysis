package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/miyako/4d-static-analysis/components/internal/fsx"
	"github.com/miyako/4d-static-analysis/components/internal/ojson"
	"github.com/miyako/4d-static-analysis/components/internal/provider"
	"github.com/miyako/4d-static-analysis/components/internal/resolver"
)

type archiveRecord struct {
	provider, repo, tag, file, sha, dir string
	size                                int64
}

// storeRecords lists the archives of the verified store.
func storeRecords(store string) []archiveRecord {
	var out []archiveRecord
	filepath.WalkDir(store, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "meta.json" {
			return nil
		}
		rel, _ := filepath.Rel(store, filepath.Dir(path))
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) < 3 {
			return nil
		}
		rec := archiveRecord{provider: parts[0], tag: parts[len(parts)-1], repo: strings.Join(parts[1:len(parts)-1], "/"), dir: filepath.Dir(path)}
		if data, err := os.ReadFile(path); err == nil {
			if v, err := ojson.Parse(ojson.StripBOM(data)); err == nil {
				if o, ok := v.(*ojson.OMap); ok {
					rec.sha = ojson.Str(o.Val("sha256"))
					rec.file = ojson.Str(o.Val("file"))
					if rec.file == "" {
						rec.file = ojson.Str(o.Val("asset"))
					}
				}
			}
		}
		out = append(out, rec)
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		return out[i].provider+"/"+out[i].repo+"/"+out[i].tag < out[j].provider+"/"+out[j].repo+"/"+out[j].tag
	})
	return out
}

func (r archiveRecord) check() string {
	if r.sha == "" || r.file == "" || r.file != filepath.Base(r.file) {
		return "metadata is incomplete or unsafe"
	}
	actual, err := fsx.SHA256File(filepath.Join(r.dir, r.file))
	if err != nil {
		return "archive is missing"
	}
	if actual != r.sha {
		return "SHA-256 does not match the recorded digest"
	}
	return ""
}

func defaultStore(f *flags) string {
	if f.archiveStore != "" {
		return f.archiveStore
	}
	home := ""
	if f.home != "" {
		home = f.home
	}
	_ = home
	return filepath.Join(fsx.UserCache(""), "miyako-4d-dependencies", "archives")
}

func runCache(args []string, stdout, stderr writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: 4d-components cache list|verify|clean [--archive-store DIR] [--all --yes]")
		return exitUsage
	}
	fs := flag.NewFlagSet("cache "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	var store string
	var all, yes bool
	fs.StringVar(&store, "archive-store", "", "archive store (default: the per-user cache)")
	fs.BoolVar(&all, "all", false, "clean: remove every archive, not only damaged ones")
	fs.BoolVar(&yes, "yes", false, "clean --all: confirm the removal of archives that a lock may still reference")
	if err := fs.Parse(args[1:]); err != nil {
		return exitUsage
	}
	if store == "" {
		store = defaultStore(&flags{})
	}
	switch args[0] {
	case "list":
		for _, r := range storeRecords(store) {
			fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", r.provider, r.repo, r.tag, r.sha)
		}
		return exitOK
	case "verify":
		bad := 0
		for _, r := range storeRecords(store) {
			if msg := r.check(); msg != "" {
				bad++
				fmt.Fprintf(stdout, "FAIL\t%s\t%s\t%s\t%s\n", r.provider, r.repo, r.tag, msg)
			} else {
				fmt.Fprintf(stdout, "ok\t%s\t%s\t%s\n", r.provider, r.repo, r.tag)
			}
		}
		if bad > 0 {
			return exitIncomplete
		}
		return exitOK
	case "clean":
		if all && !yes {
			fmt.Fprintln(stderr, "4d-components cache clean --all removes archives that a retained lock may reference: add --yes to confirm")
			return exitUsage
		}
		removed := 0
		for _, r := range storeRecords(store) {
			if all || r.check() != "" {
				if err := os.RemoveAll(r.dir); err != nil {
					fmt.Fprintf(stderr, "4d-components: %v\n", err)
					return exitInternal
				}
				removed++
			}
		}
		fmt.Fprintf(stdout, "removed %d archive(s)\n", removed)
		return exitOK
	}
	fmt.Fprintf(stderr, "4d-components cache: unknown subcommand %q\n", args[0])
	return exitUsage
}

// runVerify checks, without network access and without changing anything: the manifests parse, the last lock
// is readable, every pinned component has a stored archive that still matches its recorded SHA-256, and (with
// --components-dir) every installed component is a loadable package.
func runVerify(args []string, stdout, stderr writer) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var f flags
	f.register(fs)
	fs.StringVar(&f.componentsDir, "components-dir", "", "also check the installed components of this Components folder")
	if err := parseArgs(fs, args, &f); err != nil {
		fmt.Fprintf(stderr, "4d-components verify: %v\n", err)
		return exitUsage
	}
	f.offline = true
	f.noWriteLock = true
	m, err := f.manager(true)
	if err != nil {
		return errorExit(stderr, err)
	}
	var problems []string
	// Manifests and lock consistency: resolving offline in plan mode parses every manifest and reuses pins only.
	report, err := m.Run()
	if err != nil {
		return errorExit(stderr, err)
	}
	if l, ok := report.Val("incomplete").([]any); ok {
		for _, x := range l {
			o := x.(*ojson.OMap)
			problems = append(problems, ojson.Str(o.Val("name"))+": "+ojson.Str(o.Val("reason")))
		}
	}
	store, _ := report.Val("archiveStore").(string)
	if store == "" {
		store = defaultStore(&f)
	}
	records := map[string]archiveRecord{}
	for _, r := range storeRecords(store) {
		records[r.provider+"/"+r.repo+"/"+r.tag] = r
	}
	deps, _ := report.Val("dependencies").(*ojson.OMap)
	var installed []string
	for _, name := range deps.Keys() {
		e := deps.Val(name).(*ojson.OMap)
		var key string
		switch {
		case ojson.Truthy(e.Val("github")):
			key = "github/" + ojson.Str(e.Val("github"))
		case ojson.Truthy(e.Val("gitlab")):
			key = "gitlab/" + hostFolder(e) + ojson.Str(e.Val("gitlab"))
		default:
			continue
		}
		if ojson.Truthy(e.Val("error")) || !ojson.Truthy(e.Val("tag")) {
			continue
		}
		rec, ok := records[key+"/"+ojson.Str(e.Val("tag"))]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s: no stored archive for %s@%s", name, strings.TrimPrefix(key, ojson.Str(strings.SplitN(key, "/", 2)[0])+"/"), ojson.Str(e.Val("tag"))))
		} else if msg := rec.check(); msg != "" {
			problems = append(problems, fmt.Sprintf("%s: stored archive %s@%s: %s", name, rec.repo, rec.tag, msg))
		}
		installed = append(installed, name)
	}
	if f.componentsDir != "" {
		pkg := m.Package()
		for _, name := range installed {
			if e := deps.Val(name).(*ojson.OMap); ojson.Truthy(e.Val("overloadedBy")) {
				continue
			}
			root := filepath.Join(f.componentsDir, name+".4dbase")
			if fsx.PackageForm(root) == "" {
				problems = append(problems, fmt.Sprintf("%s: %s is missing or not a loadable package", name, filepath.Join("Components", name+".4dbase")))
			}
		}
		_ = pkg
	}
	if len(problems) == 0 {
		fmt.Fprintln(stdout, "verified: manifests parse, locks and stored archives match, installed components load")
		return exitOK
	}
	for _, p := range problems {
		fmt.Fprintln(stdout, "FAIL "+p)
	}
	if f.allowIncomplete {
		return exitOK
	}
	return exitIncomplete
}

func hostFolder(e *ojson.OMap) string {
	if h := ojson.Str(e.Val("host")); h != "" {
		return hostName(h) + "/"
	}
	return "gitlab.com/"
}

func hostName(h string) string {
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimRight(h, "/")
	if i := strings.Index(h, ":"); i >= 0 {
		h = h[:i]
	}
	return strings.ToLower(h)
}

var _ = provider.DefaultAPI
var _ = resolver.SchemaVersion
