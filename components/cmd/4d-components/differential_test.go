package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/miyako/4d-static-analysis/components/internal/fsx"
	"github.com/miyako/4d-static-analysis/components/internal/ojson"
	"github.com/miyako/4d-static-analysis/components/internal/testutil"
)

// The differential tests run the Python reference implementation (miyako/4D fetch_dependencies.py, loaded
// unmodified) and this implementation against the same mock provider with the same inputs, and compare exit
// codes, reports, lock files, the cache and archive-store trees and the provider requests. Only paths are
// normalised; real version, graph, conflict, completeness and credential differences are never normalised away.

func pythonExecutable() string {
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return "python3"
}

func referenceScript(t *testing.T) string {
	t.Helper()
	candidates := []string{os.Getenv("REF_SCRIPT"), "/home/user/miyako/4D/.github/actions/dependencies/fetch_dependencies.py"}
	for _, c := range candidates {
		if c != "" {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	t.Skip("the reference implementation is not available (set REF_SCRIPT to fetch_dependencies.py)")
	return ""
}

type rewriteTransport struct{ mock string }

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasPrefix(req.URL.Path, "/api/v4/") && req.URL.Host != strings.TrimPrefix(r.mock, "http://") {
		u, _ := url.Parse(r.mock)
		clone := req.Clone(req.Context())
		clone.URL.Scheme, clone.URL.Host, clone.Host = u.Scheme, u.Host, u.Host
		req = clone
	}
	return http.DefaultTransport.RoundTrip(req)
}

// cacheRoot is the per-user cache folder inside the isolated home, as 4D lays it out per platform.
func (s *side) cacheRoot() string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(s.home, "Library", "Caches")
	case "windows":
		return filepath.Join(s.home, "AppData", "Local")
	}
	return filepath.Join(s.home, ".cache")
}

type side struct {
	name                       string
	root, project, home, store string
	python                     bool
}

type world struct {
	t       *testing.T
	mock    *testutil.Server
	ref     string
	sides   []*side
	runtime string
}

func newWorld(t *testing.T) *world {
	w := &world{t: t, mock: testutil.New(), ref: referenceScript(t)}
	t.Cleanup(w.mock.Close)
	for _, name := range []string{"python", "native"} {
		root, _ := filepath.EvalSymlinks(t.TempDir())
		s := &side{name: name, root: root, project: filepath.Join(root, "P"), home: filepath.Join(root, "home"), store: filepath.Join(root, "store"), python: name == "python"}
		os.MkdirAll(filepath.Join(s.project, "Project", "Sources"), 0o755)
		os.WriteFile(filepath.Join(s.project, "Project", "P.4DProject"), []byte("{}"), 0o644)
		w.sides = append(w.sides, s)
	}
	testTransport = rewriteTransport{mock: w.mock.URL}
	t.Cleanup(func() { testTransport = nil })
	return w
}

// write puts a file in the project of every side (path relative to the project).
func (w *world) write(rel, content string) {
	for _, s := range w.sides {
		p := filepath.Join(s.project, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			w.t.Fatal(err)
		}
	}
}

func (w *world) declare(deps string) {
	w.write("Project/Sources/dependencies.json", `{"dependencies": `+deps+`}`)
}

type step struct {
	args     []string
	env      map[string]string
	before   func(w *world, s *side)
	wantExit int
	wantTags map[string]string // name -> resolved tag expected in the report
	noStore  bool
	rootArgs func(s *side) []string
}

type outcome struct {
	exit   int
	stderr string
	report string
	lock   string
	cache  map[string]string
	store  map[string]string
	calls  []string
	order  []string
	tags   map[string]string
}

func (s *side) args(st step) []string {
	a := []string{"--project", s.project, "--user", "u", "--home", s.home, "--output", filepath.Join(s.root, "out.json")}
	if st.noStore {
		a = append(a, "--archive-store", "none")
	} else {
		a = append(a, "--archive-store", s.store)
	}
	if st.rootArgs != nil {
		a = append(a, st.rootArgs(s)...)
	}
	return append(a, st.args...)
}

func tree(root string) map[string]string {
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, ".lock") {
			return nil
		}
		data, _ := os.ReadFile(p)
		if strings.HasSuffix(p, ".json") {
			// Python writes text files with CRLF on Windows; the JSON content is what is compared.
			data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		}
		h := sha256.Sum256(data)
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(h[:8])
		return nil
	})
	return out
}

// nativeOnly are report fields that this implementation adds to the reference's.
var nativeOnly = []string{"schemaVersion", "declaration", "declaredBy", "archiveSha256"}

func stripNative(v any) {
	switch t := v.(type) {
	case *ojson.OMap:
		for _, k := range nativeOnly {
			if t.Has(k) && (k == "schemaVersion" || k == "declaration" || k == "declaredBy" || k == "archiveSha256") {
				t.Delete(k)
			}
		}
		for _, k := range t.Keys() {
			stripNative(t.Val(k))
		}
	case []any:
		for _, x := range t {
			stripNative(x)
		}
	}
}

func (w *world) run(s *side, st step) outcome {
	t := w.t
	if st.before != nil {
		st.before(w, s)
	}
	w.mock.ResetCalls()
	out := filepath.Join(s.root, "out.json")
	os.Remove(out)
	args := s.args(st)
	var exit int
	var stderr string
	envs := map[string]string{"GITHUB_ACTIONS": "", "GITHUB_OUTPUT": "", "DEPENDENCIES_GITHUB_TOKEN": "", "DEPENDENCIES_GITLAB_TOKEN": ""}
	for k, v := range st.env {
		envs[k] = v
	}
	if s.python {
		cmd := exec.Command(pythonExecutable(), append([]string{"-I", filepath.Join("..", "..", "testdata", "ref_runner.py")}, args...)...)
		cmd.Env = append(os.Environ(), "REF_SCRIPT="+w.ref, "MOCK_URL="+w.mock.URL)
		for k, v := range envs {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		var eb bytes.Buffer
		cmd.Stderr = &eb
		err := cmd.Run()
		exit = 0
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		stderr = eb.String()
	} else {
		for k, v := range envs {
			t.Setenv(k, v)
		}
		var ob, eb bytes.Buffer
		exit = run(append(append([]string{"install"}, args...), "--github-api-url", w.mock.URL), &ob, &eb)
		stderr = eb.String()
	}
	o := outcome{exit: exit, stderr: stderr}
	if data, err := os.ReadFile(out); err == nil {
		v, err := ojson.Parse(data)
		if err != nil {
			t.Fatalf("%s report: %v\n%s", s.name, err, data)
		}
		stripNative(v)
		om := v.(*ojson.OMap)
		if deps, ok := om.Val("dependencies").(*ojson.OMap); ok {
			o.order = deps.Keys()
		}
		o.tags = map[string]string{}
		if r, ok := om.Val("resolved").(*ojson.OMap); ok {
			for _, k := range r.Keys() {
				o.tags[k] = ojson.Str(r.Val(k))
			}
		}
		b, _ := ojson.Marshal(v, "  ")
		o.report = string(b)
	}
	lock := filepath.Join(s.project, "userPreferences.u", "dependencies-lock.json")
	if data, err := os.ReadFile(lock); err == nil {
		o.lock = string(data)
	}
	o.cache, o.store = tree(s.home), tree(s.store)
	o.calls = w.mock.Paths()
	sort.Strings(o.calls)
	return o
}

var tokenRe = regexp.MustCompile(`http://127\.0\.0\.1:\d+`)

// The JSON parsers word their syntax errors differently; only that wording is normalised.
var jsonErrRe = regexp.MustCompile(`(cannot (?:parse|read) [^"\n]*?: )(Expecting[^"\n]*|invalid character[^"\n]*|unexpected[^"\n]*|Extra data[^"\n]*|Unterminated[^"\n]*|Invalid[^"\n]*|extra data[^"\n]*|JSON[^"\n]*)`)

func firstDiff(a, b string) string {
	la, lb := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(la) || i < len(lb); i++ {
		var x, y string
		if i < len(la) {
			x = la[i]
		}
		if i < len(lb) {
			y = lb[i]
		}
		if x != y {
			return fmt.Sprintf("line %d:\n  python: %s\n  native: %s", i+1, x, y)
		}
	}
	return ""
}

// rootForms are the spellings of a side's root folder in the output: the plain path, the same path JSON-escaped
// (Windows backslashes) and, on macOS, the HFS form 4D writes in the lock file.
func rootForms(root string) []string {
	forms := []string{root, strings.ReplaceAll(root, `\`, `\\`), strings.ReplaceAll(root, `\`, "/")}
	if runtime.GOOS == "darwin" {
		forms = append([]string{strings.TrimSuffix(fsx.NativePath(root), ":")}, forms...)
	}
	return forms
}

func (w *world) normalise(s *side, text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	for _, form := range rootForms(s.root) {
		text = strings.ReplaceAll(text, form, "<ROOT>")
	}
	text = tokenRe.ReplaceAllString(text, "<MOCK>")
	text = jsonErrRe.ReplaceAllString(text, "${1}<json error>")
	text = strings.ReplaceAll(text, "fetch_dependencies:", "<tool>:")
	text = strings.ReplaceAll(text, "4d-components:", "<tool>:")
	return text
}

// Python's report order of `dependencies` keys and ours both follow resolution order; the JSON text is compared
// with key order significant for that object only through `order`.
func compare(t *testing.T, w *world, step int, py, nat outcome) {
	t.Helper()
	pys, nas := w.sides[0], w.sides[1]
	check := func(what, a, b string) {
		a, b = w.normalise(pys, a), w.normalise(nas, b)
		if a != b {
			t.Errorf("step %d: %s differs at %s", step, what, firstDiff(a, b))
		}
	}
	if py.exit != nat.exit {
		t.Errorf("step %d: exit python=%d native=%d\npython stderr: %s\nnative stderr: %s", step, py.exit, nat.exit, py.stderr, nat.stderr)
	}
	parse := func(sd *side, text string) string {
		v, err := ojson.Parse([]byte(w.normalise(sd, text)))
		if err != nil {
			return text
		}
		// order-insensitive canonical form: compare via Equal below, but print compactly
		b, _ := ojson.Marshal(v, "")
		return string(b)
	}
	if py.report != "" || nat.report != "" {
		pv, _ := ojson.Parse([]byte(w.normalise(pys, py.report)))
		nv, _ := ojson.Parse([]byte(w.normalise(nas, nat.report)))
		if !ojson.Equal(pv, nv) {
			t.Errorf("step %d: report differs at %s", step, firstDiff(strings.ReplaceAll(parse(pys, py.report), ", \"", ",\n\""), strings.ReplaceAll(parse(nas, nat.report), ", \"", ",\n\"")))
		}
	}
	check("lock file", py.lock, nat.lock)
	check("stderr", py.stderr, nat.stderr)
	check("dependency order", strings.Join(py.order, ","), strings.Join(nat.order, ","))
	check("provider requests", strings.Join(py.calls, "\n"), strings.Join(nat.calls, "\n"))
	for _, p := range []struct {
		name string
		a, b map[string]string
	}{{"cache tree", py.cache, nat.cache}, {"archive store tree", py.store, nat.store}} {
		var ka, kb []string
		for k, v := range p.a {
			ka = append(ka, k+"="+v)
		}
		for k, v := range p.b {
			kb = append(kb, k+"="+v)
		}
		sort.Strings(ka)
		sort.Strings(kb)
		check(p.name, strings.Join(ka, "\n"), strings.Join(kb, "\n"))
	}
}

func zipOf(name string) []byte { return testutil.ComponentZip(name, true, nil) }

func addRepo(w *world, repo string, tags ...string) *testutil.Repo {
	r := &testutil.Repo{Children: map[string]string{}}
	name := repo[strings.Index(repo, "/")+1:]
	for _, tag := range tags {
		r.Releases = append(r.Releases, testutil.Rel{Tag: tag, Zip: zipOf(name)})
	}
	w.mock.Repos[repo] = r
	return r
}

type scenario struct {
	name  string
	setup func(w *world)
	steps []step
}

func runScenarios(t *testing.T, list []scenario) {
	for _, sc := range list {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			w := newWorld(t)
			sc.setup(w)
			for i, st := range sc.steps {
				py := w.run(w.sides[0], st)
				nat := w.run(w.sides[1], st)
				compare(t, w, i+1, py, nat)
				if nat.exit != st.wantExit {
					t.Errorf("step %d: native exit %d, want %d\n%s", i+1, nat.exit, st.wantExit, nat.stderr)
				}
				for name, tag := range st.wantTags {
					if nat.tags[name] != tag {
						t.Errorf("step %d: %s resolved to %q, want %q", i+1, name, nat.tags[name], tag)
					}
				}
				if t.Failed() {
					return
				}
			}
		})
	}
}

var _ = fmt.Sprintf
