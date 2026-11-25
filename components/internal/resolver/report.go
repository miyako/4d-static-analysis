package resolver

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/miyako/4d-static-analysis/components/internal/fsx"
	"github.com/miyako/4d-static-analysis/components/internal/ojson"
)

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (m *Manager) report(environment string) (*ojson.OMap, error) {
	resolved := ojson.New()
	names := append([]string(nil), m.order...)
	sort.Strings(names)
	for _, n := range names {
		e := m.results[n]
		if ojson.Truthy(e.Val("tag")) && !ojson.Truthy(e.Val("error")) {
			resolved.Set(n, e.Val("tag"))
		}
	}
	deps := ojson.New()
	for _, n := range m.order {
		deps.Set(n, m.results[n])
	}
	env := ojson.New()
	if m.home != "" {
		vars, err := fsx.IsolationEnv(m.home)
		if err != nil {
			return nil, err
		}
		keys := make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range []string{"HOME", "CFFIXED_USER_HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "XDG_CACHE_HOME"} {
			if v, ok := vars[k]; ok {
				env.Set(k, v)
			}
		}
	}
	warnings := make([]any, len(m.warnings))
	for i, w := range m.warnings {
		warnings[i] = w
	}
	incomplete := m.incomplete
	if incomplete == nil {
		incomplete = []any{}
	}
	lockFile := any(nil)
	if !m.opts.NoWriteLock && !m.opts.PlanOnly {
		lockFile = m.lockPath
	}
	r := ojson.New()
	r.Set("schemaVersion", ojson.Int(SchemaVersion))
	r.Set("runtime", m.opts.Runtime).Set("runtimeVersion", nilIfEmpty(m.opts.RuntimeVersion))
	r.Set("platform", fsx.PlatformName()).Set("package", m.pkg)
	r.Set("dependenciesFolder", m.depsFolder).Set("home", nilIfEmpty(m.home)).Set("env", env)
	r.Set("archiveStore", nilIfEmpty(m.store)).Set("lockFile", lockFile)
	r.Set("environment4d", nilIfEmpty(environment))
	r.Set("token", m.github.HasToken()).Set("gitlabToken", m.gitlabToken).Set("offline", m.github.Offline())
	r.Set("resolved", resolved).Set("dependencies", deps).Set("incomplete", incomplete).Set("warnings", warnings)
	return r, nil
}

// rewrite writes a flat exact-tag dependencies.json into a *workspace copy* of the package: tool4d loads exact
// `tag` entries only and never resolves children.
func (m *Manager) rewrite(pkg, environment string, envEntries *ojson.OMap) error {
	target := filepath.Join(pkg, "Project", "Sources", "dependencies.json")
	original := ojson.New()
	if isFile(target) {
		v, err := loadJSON(target)
		if err != nil {
			return err
		}
		o, ok := v.(*ojson.OMap)
		if !ok {
			return usage("invalid %s: the top level must be a JSON object", target)
		}
		original = o
	}
	deps := ojson.New()
	if d, ok := original.Val("dependencies").(*ojson.OMap); ok {
		deps = d.Copy()
	}
	for _, name := range m.order {
		e := m.results[name]
		if !ojson.Truthy(e.Val("tag")) || ojson.Truthy(e.Val("error")) || envEntries.Has(name) {
			continue
		}
		var key string
		switch {
		case ojson.Truthy(e.Val("github")):
			key = "github"
		case ojson.Truthy(e.Val("gitlab")):
			key = "gitlab"
		default:
			continue
		}
		item := ojson.New().Set(key, e.Val(key)).Set("tag", e.Val("tag"))
		if old, ok := deps.Val(name).(*ojson.OMap); ok {
			for _, k := range old.Keys() {
				if k != "version" && k != "tag" && k != key {
					item.Set(k, old.Val(k))
				}
			}
		}
		deps.Set(name, item)
	}
	original.Set("dependencies", deps)
	if err := fsx.WriteJSON(target, original, "  "); err != nil {
		return err
	}
	var githubEnv []string
	for _, n := range envEntries.Keys() {
		if s, ok := envEntries.Val(n).(*ojson.OMap); ok && ojson.Truthy(s.Val("github")) {
			githubEnv = append(githubEnv, n)
		}
	}
	if len(githubEnv) == 0 {
		return nil
	}
	copyEnv := filepath.Join(pkg, "environment4d.json")
	if !isFile(copyEnv) {
		m.warn("environment4d.json GitHub entries %s are outside %s; tool4d may not load them unless they use exact tags", pyList(githubEnv), pkg)
		return nil
	}
	v, err := loadJSON(copyEnv)
	if err != nil {
		return err
	}
	value, ok := v.(*ojson.OMap)
	if !ok {
		return nil
	}
	envDeps, _ := value.Val("dependencies").(*ojson.OMap)
	if envDeps == nil {
		return nil
	}
	for _, name := range githubEnv {
		e := m.results[name]
		if e != nil && ojson.Truthy(e.Val("tag")) && !ojson.Truthy(e.Val("error")) {
			envDeps.Set(name, ojson.New().Set("github", e.Val("github")).Set("tag", e.Val("tag")))
		}
	}
	return fsx.WriteJSON(copyEnv, value, "  ")
}

func pyList(items []string) string {
	out := "["
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += ojson.Repr(s)
	}
	return out + "]"
}

var _ = os.Stat

// annotate adds the native extension fields to every entry: the declaration as written and the components that
// declare it. They are additions to the reference implementation's report, which the parity comparison ignores.
func (m *Manager) annotate(declared *ojson.OMap) {
	for _, name := range m.order {
		e := m.results[name]
		if declared.Has(name) {
			e.Set("declaration", declared.Val(name))
		}
		if parents := m.declarers[name]; len(parents) > 0 {
			list := make([]any, len(parents))
			for i, p := range parents {
				list[i] = p
			}
			e.Set("declaredBy", list)
		}
	}
}
