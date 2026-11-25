// Package resolver is the dependency graph engine: it resolves the declarations of a 4D project (and, for GitHub
// components, of their repositories) against provider releases the way the real 4D dependency manager does,
// materialises verified archives in the 4D cache layout and writes the 4D-compatible lock file.
//
// Every rule follows an observed 4D behaviour recorded in docs/component-manager-behavior.md; none of it is a
// generic SemVer solver.
package resolver

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/miyako/4d-static-analysis/components/internal/fsx"
	"github.com/miyako/4d-static-analysis/components/internal/ojson"
	"github.com/miyako/4d-static-analysis/components/internal/provider"
	"github.com/miyako/4d-static-analysis/components/internal/version"
)

const (
	UnverifiedReuse = "unverified (4D-equivalent)"
	SchemaVersion   = 1
)

// Runtimes maps the accepted runtime spellings to the cache folder name.
var Runtimes = map[string]string{"tool4d": "tool4d", "4d": "4D", "4d server": "4D Server"}

// Options configure one run. Zero values mean "default".
type Options struct {
	Project            string
	Runtime            string // canonical: tool4d | 4D | 4D Server
	RuntimeVersion     string
	Home               string
	DependenciesFolder string
	ArchiveStore       string // "" = default (on when Home is set), "none" = off
	Update             bool
	NoWriteLock        bool
	User               string
	Rewrite            string
	// PlanOnly resolves without installing: no archive is downloaded or extracted and the cache is not touched.
	PlanOnly bool
}

// Manager resolves one project.
type Manager struct {
	opts          Options
	github        provider.Client
	gitlabFactory func(host string) provider.Client
	gitlabClients map[string]provider.Client
	gitlabToken   bool

	anchor      string
	versionCode int
	home        string
	depsFolder  string
	store       string
	pkg         string
	lockPath    string

	results       map[string]*ojson.OMap
	order         []string
	incomplete    []any
	warnings      []string
	combined      map[string][]any
	declarers     map[string][]string
	conflicts     map[string]string
	conflictOrder []string
	pathBases     map[string]string
}

var (
	githubRepoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	gitlabRepoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+$`)
	gitlabHostRe = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(:\d+)?/?$`)
)

func usage(format string, args ...any) error {
	return &version.UsageError{Msg: fmt.Sprintf(format, args...)}
}

func failf(format string, args ...any) error {
	return &version.Failure{Msg: fmt.Sprintf(format, args...)}
}

func resolvePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

func currentUser() string {
	for _, k := range []string{"LOGNAME", "USER", "LNAME", "USERNAME"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	if u, err := user.Current(); err == nil {
		name := u.Username
		if i := strings.LastIndex(name, `\`); i >= 0 {
			name = name[i+1:]
		}
		return name
	}
	return "user"
}

// New prepares a manager. gitlabFactory builds a client for a GitLab host base URL; nil disables GitLab
// token handling defaults.
func New(opts Options, gh provider.Client, gitlabFactory func(host string) provider.Client, gitlabToken bool) (*Manager, error) {
	m := &Manager{opts: opts, github: gh, gitlabFactory: gitlabFactory, gitlabClients: map[string]provider.Client{},
		gitlabToken: gitlabToken, combined: map[string][]any{}}
	if opts.RuntimeVersion != "" {
		a, code, err := version.RuntimeAnchor(opts.RuntimeVersion)
		if err != nil {
			return nil, err
		}
		m.anchor, m.versionCode = a, code
	}
	if opts.Home != "" {
		m.home = resolvePath(opts.Home)
	}
	if opts.DependenciesFolder != "" {
		m.depsFolder = resolvePath(opts.DependenciesFolder)
	} else {
		m.depsFolder = filepath.Join(fsx.UserCache(m.home), opts.Runtime, "Dependencies")
	}
	if opts.ArchiveStore != "none" && (opts.ArchiveStore != "" || m.home != "") {
		if opts.ArchiveStore != "" {
			m.store = resolvePath(opts.ArchiveStore)
		} else {
			m.store = filepath.Join(fsx.UserCache(""), "miyako-4d-dependencies", "archives")
		}
	}
	pkg, err := LocateProject(opts.Project)
	if err != nil {
		return nil, err
	}
	m.pkg = pkg
	u := opts.User
	if u == "" {
		u = currentUser()
	}
	m.lockPath = filepath.Join(pkg, "userPreferences."+u, "dependencies-lock.json")
	return m, nil
}

// Package is the resolved project package folder.
func (m *Manager) Package() string { return m.pkg }

// LockPath is the 4D-compatible lock file location.
func (m *Manager) LockPath() string { return m.lockPath }

// LocateProject finds the package folder of a project given as a folder or a .4DProject file.
func LocateProject(path string) (string, error) {
	path = resolvePath(path)
	st, err := os.Stat(path)
	if err == nil && st.Mode().IsRegular() && strings.EqualFold(filepath.Ext(path), ".4dproject") {
		return filepath.Dir(filepath.Dir(path)), nil
	}
	if err == nil && st.IsDir() {
		matches, _ := filepath.Glob(filepath.Join(path, "Project", "*.4DProject"))
		if len(matches) > 0 {
			return path, nil
		}
	}
	return "", usage("no 4D project at %s", path)
}

// ---------------------------------------------------------------- manifests

func loadJSON(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, usage("cannot read %s: %v", path, err)
	}
	v, err := ojson.Parse(ojson.StripBOM(data))
	if err != nil {
		return nil, usage("cannot read %s: %v", path, err)
	}
	return v, nil
}

func loadManifest(path string) (*ojson.OMap, error) {
	v, err := loadJSON(path)
	if err != nil {
		return nil, err
	}
	deps, err := provider.ManifestDependencies(v)
	if err != nil {
		return nil, usage("invalid %s: %v", path, err)
	}
	return deps, nil
}

func isFile(p string) bool { st, err := os.Stat(p); return err == nil && st.Mode().IsRegular() }
func isDir(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }

// FindEnvironment returns environment4d.json from the package folder or the nearest parent folder.
func FindEnvironment(pkg string) string {
	for folder := pkg; ; folder = filepath.Dir(folder) {
		if c := filepath.Join(folder, "environment4d.json"); isFile(c) {
			return c
		}
		if filepath.Dir(folder) == folder {
			return ""
		}
	}
}

func componentOverride(pkg, name string) string {
	for _, c := range []string{name + ".4dbase", name + ".4DZ", name} {
		if _, err := os.Lstat(filepath.Join(pkg, "Components", c)); err == nil {
			return "Components/" + c
		}
	}
	return ""
}

func (m *Manager) warn(format string, args ...any) {
	m.warnings = append(m.warnings, fmt.Sprintf(format, args...))
}

func (m *Manager) declarations() (*ojson.OMap, string, *ojson.OMap, error) {
	declared := ojson.New()
	sources := filepath.Join(m.pkg, "Project", "Sources", "dependencies.json")
	if isFile(sources) {
		deps, err := loadManifest(sources)
		if err != nil {
			return nil, "", nil, err
		}
		declared = deps.Copy()
	}
	environment := FindEnvironment(m.pkg)
	envEntries := ojson.New()
	if environment != "" {
		deps, err := loadManifest(environment)
		if err != nil {
			return nil, "", nil, err
		}
		for _, name := range deps.Keys() {
			entry := deps.Val(name)
			if !declared.Has(name) {
				// 4D 21.2 and 21R5 neither fetch nor load a name declared only here.
				m.warn("%s: declared only in %s; 4D ignores it", name, environment)
				continue
			}
			envEntries.Set(name, entry)
			if s, ok := entry.(string); ok {
				declared.Set(name, ojson.New().Set("path", s))
			} else {
				declared.Set(name, entry)
			}
		}
	}
	// A relative path resolves from the folder that contains environment4d.json, or otherwise from the folder
	// that contains the project package.
	m.pathBases = map[string]string{}
	for _, name := range envEntries.Keys() {
		m.pathBases[name] = filepath.Dir(environment)
	}
	return declared, environment, envEntries, nil
}

// lockPins reads the tags pinned by the last lock file. Explicit update ignores them.
func (m *Manager) lockPins() map[string]*ojson.OMap {
	pins := map[string]*ojson.OMap{}
	if m.opts.Update || !isFile(m.lockPath) {
		return pins
	}
	unreadable := func() map[string]*ojson.OMap {
		m.warn("ignored unreadable lock file %s", m.lockPath)
		return map[string]*ojson.OMap{}
	}
	data, err := os.ReadFile(m.lockPath)
	if err != nil {
		return unreadable()
	}
	v, err := ojson.Parse(ojson.StripBOM(data))
	if err != nil {
		return unreadable()
	}
	top, ok := v.(*ojson.OMap)
	if !ok {
		return unreadable()
	}
	deps := top.Val("dependencies")
	if !ojson.Truthy(deps) {
		return pins
	}
	dm, ok := deps.(*ojson.OMap)
	if !ok {
		return unreadable()
	}
	for _, n := range dm.Keys() {
		if e, ok := dm.Val(n).(*ojson.OMap); ok && ojson.Truthy(e.Val("tag")) && (ojson.Truthy(e.Val("github")) || ojson.Truthy(e.Val("gitlab"))) {
			pins[n] = e
		}
	}
	return pins
}

// ---------------------------------------------------------------- run

// Run resolves the project and returns the JSON report. UsageError means the run could not be performed.
func (m *Manager) Run() (*ojson.OMap, error) {
	declared, environment, envEntries, err := m.declarations()
	if err != nil {
		return nil, err
	}
	pins := m.lockPins()
	baseWarnings := append([]string(nil), m.warnings...)
	settled := false
	// A secondary declaration that the first resolution of its name does not satisfy is resolved together with
	// the earlier ones, which can change the tag and so the children: resolve again from the start.
	for pass := 0; pass < 64; pass++ {
		m.results, m.order, m.incomplete = map[string]*ojson.OMap{}, nil, nil
		m.warnings = append([]string(nil), baseWarnings...)
		m.declarers, m.conflicts, m.conflictOrder = map[string][]string{}, map[string]string{}, nil
		again, err := m.resolveAll(declared, pins)
		if err != nil {
			return nil, err
		}
		if !again {
			settled = true
			break
		}
	}
	if !settled {
		return nil, usage("secondary dependency conflicts did not settle")
	}
	m.annotate(declared)
	if !m.opts.NoWriteLock && !m.opts.PlanOnly {
		if err := m.writeLock(); err != nil {
			return nil, err
		}
	}
	if m.opts.Rewrite != "" && !m.opts.PlanOnly {
		if err := m.rewrite(resolvePath(m.opts.Rewrite), environment, envEntries); err != nil {
			return nil, err
		}
	}
	return m.report(environment)
}

type queued struct {
	name       string
	spec       any
	primary    bool
	base       string
	declaredBy string
	parent     string
	hasParent  bool
}

func (m *Manager) setResult(name string, e *ojson.OMap) {
	if _, ok := m.results[name]; !ok {
		m.order = append(m.order, name)
	}
	m.results[name] = e
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// resolveAll resolves breadth first; true means a new secondary conflict needs another pass.
func (m *Manager) resolveAll(declared *ojson.OMap, pins map[string]*ojson.OMap) (bool, error) {
	host := filepath.Dir(m.pkg)
	var queue []queued
	for _, name := range declared.Keys() {
		base := host
		if b, ok := m.pathBases[name]; ok {
			base = b
		}
		queue = append(queue, queued{name: name, spec: declared.Val(name), primary: true, base: base})
	}
	specs := map[string]any{}
	for len(queue) > 0 {
		q := queue[0]
		queue = queue[1:]
		if q.hasParent && !contains(m.declarers[q.name], q.parent) {
			m.declarers[q.name] = append(m.declarers[q.name], q.parent)
		}
		if previous, seen := m.results[q.name]; seen {
			qspec, isDict := q.spec.(*ojson.OMap)
			_, inCombined := m.combined[q.name]
			if !isDict || q.primary || previous.Val("primary") == true || inCombined {
				if inCombined && isDict {
					found := false
					for _, s := range m.combined[q.name] {
						if ojson.Equal(s, q.spec) {
							found = true
						}
					}
					if !found {
						m.combined[q.name] = append(m.combined[q.name], q.spec)
						return true, nil
					}
				}
				continue
			}
			if ojson.Equal(q.spec, specs[q.name]) {
				continue
			}
			first, _ := specs[q.name].(*ojson.OMap)
			if !ojson.Truthy(qspec.Val("github")) || first == nil || !ojson.Truthy(first.Val("github")) {
				m.warn("%s: secondary declaration %s differs from %s; the first is kept (not observed in 4D)",
					q.name, ojson.PyDumps(q.spec), ojson.PyDumps(specs[q.name]))
				continue
			}
			// Differing secondary declarations are settled together (combinedRelease): the result can change
			// the tag and so the children, so resolve again from the start.
			m.combined[q.name] = []any{specs[q.name], q.spec}
			return true, nil
		}
		var entry *ojson.OMap
		if _, inCombined := m.combined[q.name]; !q.primary && inCombined {
			entry = m.resolveOne(q.name, nil, false, pins, m.combined[q.name], "", "")
		} else {
			entry = m.resolveOne(q.name, q.spec, q.primary, pins, nil, q.base, q.declaredBy)
			specs[q.name] = q.spec
		}
		m.setResult(q.name, entry)
		// Paths declared by a local component also resolve from the host package's parent folder. How 4D
		// resolves a path declared by a GitHub component has not been observed.
		local := entry.Val("source") == "local"
		origin := ""
		if !local {
			src := entry.Val("github")
			if !ojson.Truthy(src) {
				src = entry.Val("gitlab")
			}
			origin = fmt.Sprintf("%s@%s", ojson.Str(src), ojson.Str(entry.Val("tag")))
		}
		if children, ok := entry.Val("dependencies").(*ojson.OMap); ok {
			for _, child := range children.Keys() {
				base := ""
				if local {
					base = host
				}
				queue = append(queue, queued{name: child, spec: children.Val(child), base: base, declaredBy: origin, parent: q.name, hasParent: true})
			}
		}
	}
	// A dependency whose declarations conflict is loaded as first declared, but none of the components that
	// declare it (errCode 21).
	for _, name := range m.conflictOrder {
		for _, parent := range m.declarers[name] {
			if e, ok := m.results[parent]; ok {
				m.disable(e, name)
			}
		}
	}
	return false, nil
}

func (m *Manager) fail(e *ojson.OMap, reason string) *ojson.OMap {
	e.Set("error", reason)
	m.incomplete = append(m.incomplete, ojson.New().Set("name", e.Val("name")).Set("reason", reason))
	return e
}

func (m *Manager) disable(e *ojson.OMap, dependency string) {
	if ojson.Truthy(e.Val("disabled")) {
		return
	}
	reason := fmt.Sprintf("not loaded by 4D: its dependency %s is disabled by a version conflict", dependency)
	e.Set("disabled", true)
	appendError(e, ojson.New().Set("errCode", ojson.Int(21)).Set("message", reason))
	if !ojson.Truthy(e.Val("error")) {
		m.fail(e, reason)
	}
}

func appendError(e *ojson.OMap, item *ojson.OMap) {
	list, _ := e.Val("errors").([]any)
	e.Set("errors", append(list, item))
}

func specOf(spec *ojson.OMap) version.Spec {
	s := version.Spec{}
	if ojson.Truthy(spec.Val("tag")) {
		s.Tag = ojson.Str(spec.Val("tag"))
	}
	if v := spec.Val("version"); v != nil {
		s.HasVersion, s.Version = true, ojson.Str(v)
	}
	return s
}

func (m *Manager) classify(spec *ojson.OMap, gitlab bool) (version.Decl, error) {
	return version.Classify(specOf(spec), m.anchor, gitlab)
}

// pick resolves one declaration on its own: tag, release (nil when no lookup was needed), lock version, pinned.
func (m *Manager) pick(name string, src *Source, spec *ojson.OMap, pins map[string]*ojson.OMap) (string, *provider.Release, string, bool, error) {
	d, err := m.classify(spec, src.gitlab())
	if err != nil {
		return "", nil, "", false, err
	}
	if pin := pins[name]; d.Kind != version.KindTag && pin != nil && src.matches(pin) {
		pinVersion := ""
		if ojson.Truthy(pin.Val("version")) {
			pinVersion = ojson.Str(pin.Val("version"))
		}
		pinTag := ojson.Str(pin.Val("tag"))
		if pinVersion == d.Lock && (d.Kind != version.KindRange || version.Satisfies(pinTag, d.Ranges)) {
			return pinTag, nil, d.Lock, true, nil
		}
	}
	if d.Kind == version.KindTag {
		return d.Tag, nil, d.Lock, false, nil
	}
	rel, err := src.Client.Release(src.Repo, d)
	if err != nil {
		return "", nil, "", false, err
	}
	return rel.Tag, rel, d.Lock, false, nil
}

func (m *Manager) resolveLocal(e *ojson.OMap, spec *ojson.OMap, base, declaredBy string) *ojson.OMap {
	name := ojson.Str(e.Val("name"))
	rawV, has := spec.Get("path")
	if !has {
		rawV = ""
	}
	e.Set("source", "local")
	e.Set("local", rawV)
	if declaredBy != "" {
		m.warn("%s: local path declared by %s is not resolved; how 4D resolves it has not been observed", name, declaredBy)
		return e
	}
	folder := ""
	if s, ok := rawV.(string); ok && s != "" {
		folder = s
		if !filepath.IsAbs(folder) {
			folder = filepath.Join(base, folder)
		}
	}
	if folder == "" || !isDir(folder) {
		// 4D writes path "" and found false.
		m.warn("%s: local component not found at %s", name, ojson.Repr(rawV))
		return e
	}
	folder = filepath.Clean(folder)
	e.Set("path", folder)
	manifest := filepath.Join(folder, "Project", "Sources", "dependencies.json")
	if !isFile(manifest) {
		return e
	}
	data, err := os.ReadFile(manifest)
	var v any
	if err == nil {
		v, err = ojson.Parse(ojson.StripBOM(data))
	}
	if err != nil {
		return m.fail(e, fmt.Sprintf("cannot parse %s: %v", manifest, err))
	}
	deps, err := provider.ManifestDependencies(v)
	if err != nil {
		return m.fail(e, fmt.Sprintf("invalid %s: %v", manifest, err))
	}
	if deps.Len() == 0 {
		e.Set("dependencies", nil)
	} else {
		e.Set("dependencies", deps)
	}
	return e
}

func (m *Manager) resolveOne(name string, specAny any, primary bool, pins map[string]*ojson.OMap, combined []any, base, declaredBy string) *ojson.OMap {
	e := ojson.New().Set("name", name).Set("primary", primary)
	if len(combined) > 0 {
		specAny = combined[0]
		e.Set("declarations", combined)
	}
	spec, ok := specAny.(*ojson.OMap)
	if !ok {
		return m.fail(e, fmt.Sprintf("invalid declaration %s: expected a JSON object", ojson.PyDumps(specAny)))
	}
	if !safeSegment(name) {
		return m.fail(e, fmt.Sprintf("unsafe component name %s", ojson.Repr(name)))
	}
	if ojson.Truthy(spec.Val("gitlab")) {
		return m.resolveGitLab(e, spec, pins)
	}
	if !ojson.Truthy(spec.Val("github")) {
		if base == "" {
			base = filepath.Dir(m.pkg)
		}
		return m.resolveLocal(e, spec, base, declaredBy)
	}
	repo := strings.Trim(ojson.Str(spec.Val("github")), "/")
	if !githubRepoRe.MatchString(repo) || !safeRepo(repo) {
		return m.fail(e, fmt.Sprintf("invalid github repository %s", ojson.Repr(repo)))
	}
	e.Set("github", repo)
	src := &Source{Provider: "github", Repo: repo, Client: m.github}
	var tag, lockVersion string
	var release *provider.Release
	var err error
	if len(combined) > 0 {
		var conflict string
		tag, release, lockVersion, conflict, err = m.combinedRelease(name, repo, combined, pins)
		if err == nil && conflict != "" {
			if _, seen := m.conflicts[name]; !seen {
				m.conflictOrder = append(m.conflictOrder, name)
			}
			m.conflicts[name] = conflict
			appendError(e, ojson.New().Set("message", conflict))
		}
	} else {
		var pinned bool
		tag, release, lockVersion, pinned, err = m.pick(name, src, spec, pins)
		if err == nil && pinned {
			e.Set("pinned", true)
		}
	}
	if err == nil {
		if lockVersion != "" {
			e.Set("version", lockVersion)
		}
		e.Set("tag", tag)
		err = m.materialise(e, src, tag, release)
	}
	if err != nil {
		return m.failFrom(e, err)
	}
	return m.finish(e, name)
}

// failFrom makes the component incomplete for a dependency-level failure; anything else stops the run.
func (m *Manager) failFrom(e *ojson.OMap, err error) *ojson.OMap {
	return m.fail(e, err.Error())
}

func isFailure(err error) bool {
	var f *version.Failure
	return errors.As(err, &f)
}

func (m *Manager) finish(e *ojson.OMap, name string) *ojson.OMap {
	if override := componentOverride(m.pkg, name); override != "" {
		e.Set("overloadedBy", override)
		appendError(e, ojson.New().Set("errCode", ojson.Int(19)).
			Set("message", fmt.Sprintf("A component with the same name (%s) is already loaded from %s", name, override)))
	}
	return e
}
