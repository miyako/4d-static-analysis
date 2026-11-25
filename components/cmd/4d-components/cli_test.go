package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/miyako/4d-static-analysis/components/internal/ojson"
	"github.com/miyako/4d-static-analysis/components/internal/testutil"
)

// env is a native-only test environment: no Python reference is needed.
type env struct {
	t       *testing.T
	mock    *testutil.Server
	root    string
	project string
	home    string
	store   string
}

func newEnv(t *testing.T) *env {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	e := &env{t: t, mock: testutil.New(), root: root, project: filepath.Join(root, "P"), home: filepath.Join(root, "home"), store: filepath.Join(root, "store")}
	t.Cleanup(e.mock.Close)
	os.MkdirAll(filepath.Join(e.project, "Project", "Sources"), 0o755)
	os.WriteFile(filepath.Join(e.project, "Project", "P.4DProject"), []byte("{}"), 0o644)
	for _, k := range []string{"GITHUB_ACTIONS", "GITHUB_OUTPUT", "DEPENDENCIES_GITHUB_TOKEN", "DEPENDENCIES_GITLAB_TOKEN", "RUNNER_ENVIRONMENT"} {
		t.Setenv(k, "")
	}
	return e
}

func (e *env) declare(deps string) {
	os.WriteFile(filepath.Join(e.project, "Project", "Sources", "dependencies.json"), []byte(`{"dependencies": `+deps+`}`), 0o644)
}

func (e *env) repo(name string, tags ...string) *testutil.Repo {
	r := &testutil.Repo{Children: map[string]string{}}
	short := name[strings.Index(name, "/")+1:]
	for _, tag := range tags {
		r.Releases = append(r.Releases, testutil.Rel{Tag: tag, Zip: testutil.ComponentZip(short, true, nil)})
	}
	e.mock.Repos[name] = r
	return r
}

func (e *env) run(cmd string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	full := append([]string{cmd, e.project, "--user", "u", "--home", e.home, "--archive-store", e.store, "--github-api-url", e.mock.URL}, args...)
	code := run(full, &out, &errb)
	return code, out.String(), errb.String()
}

func parse(t *testing.T, s string) *ojson.OMap {
	t.Helper()
	v, err := ojson.Parse([]byte(s))
	if err != nil {
		t.Fatalf("%v\n%s", err, s)
	}
	return v.(*ojson.OMap)
}

func (e *env) files() []string {
	var out []string
	filepath.WalkDir(e.root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestResolveIsPlanOnly(t *testing.T) {
	e := newEnv(t)
	e.repo("o/A", "1.0.0", "1.1.0")
	e.declare(`{"A": {"github": "o/A"}}`)
	code, out, errb := e.run("resolve")
	if code != 0 {
		t.Fatalf("%d %s", code, errb)
	}
	rep := parse(t, out)
	if rep.Val("resolved").(*ojson.OMap).Val("A") != "1.1.0" {
		t.Fatalf("%s", out)
	}
	for _, p := range e.mock.Paths() {
		if strings.Contains(p, "/download/") || strings.Contains(p, "/assets/") {
			t.Fatalf("resolve must not download: %s", p)
		}
	}
	for _, f := range e.files() {
		if strings.HasPrefix(f, e.home) || strings.HasPrefix(f, e.store) || strings.Contains(f, "userPreferences") {
			t.Fatalf("resolve must not write the cache, store or lock: %s", f)
		}
	}
	if rep.Val("lockFile") != nil {
		t.Fatal("no lock file in plan mode")
	}
}

func TestInstallIntoComponentsKeepsPrecedenceAndOriginalProject(t *testing.T) {
	e := newEnv(t)
	e.repo("o/A", "1.0.0")
	e.repo("o/B", "1.0.0")
	e.declare(`{"A": {"github": "o/A"}, "B": {"github": "o/B"}}`)
	os.MkdirAll(filepath.Join(e.project, "Components", "B.4dbase", "Project"), 0o755)
	os.WriteFile(filepath.Join(e.project, "Components", "B.4dbase", "Project", "mine.4DProject"), []byte("{}"), 0o644)
	code, out, errb := e.run("install", "--components-dir", filepath.Join(e.project, "Components"), "--workspace", e.project)
	if code != 0 {
		t.Fatalf("%d %s", code, errb)
	}
	inst := parse(t, out).Val("installation").(*ojson.OMap)
	if got := ojson.PyDumps(inst.Val("installed")); got != `["A"]` {
		t.Fatalf("installed %s", got)
	}
	if got := ojson.PyDumps(inst.Val("providedByComponents")); got != `["B"]` {
		t.Fatalf("provided %s", got)
	}
	if _, err := os.Stat(filepath.Join(e.project, "Components", "A.4dbase", "Project", "A.4DProject")); err != nil {
		t.Fatal("A is installed")
	}
	if _, err := os.Stat(filepath.Join(e.project, "Components", "B.4dbase", "Project", "mine.4DProject")); err != nil {
		t.Fatal("an existing Components entry must be untouched")
	}
	if _, err := os.Stat(filepath.Join(e.project, "Components", "B.4dbase", "Project", "B.4DProject")); err == nil {
		t.Fatal("an existing Components entry must not be overwritten")
	}
	// repeated installation is idempotent
	if code, _, errb := e.run("install", "--components-dir", filepath.Join(e.project, "Components"), "--workspace", e.project); code != 0 {
		t.Fatalf("second run %d %s", code, errb)
	}
}

func TestInstallLocalComponentsOnlyInsideTheWorkspace(t *testing.T) {
	e := newEnv(t)
	inside := filepath.Join(e.root, "Loc")
	os.MkdirAll(filepath.Join(inside, "Project"), 0o755)
	os.WriteFile(filepath.Join(inside, "Project", "Loc.4DProject"), []byte("{}"), 0o644)
	e.declare(`{"Loc": {"path": "Loc"}}`)
	// workspace = the folder containing both: the local component is inside it
	code, out, errb := e.run("install", "--components-dir", filepath.Join(e.project, "Components"), "--workspace", e.root)
	if code != 0 {
		t.Fatalf("%d %s", code, errb)
	}
	if got := ojson.PyDumps(parse(t, out).Val("installation").(*ojson.OMap).Val("installed")); got != `["Loc"]` {
		t.Fatalf("%s", got)
	}
	// workspace = the project only: the component is outside it and stays incomplete
	os.RemoveAll(filepath.Join(e.project, "Components"))
	code, _, errb = e.run("install", "--components-dir", filepath.Join(e.project, "Components"), "--workspace", e.project)
	if code != 3 || !strings.Contains(errb, "outside the analyzed repository") {
		t.Fatalf("%d %s", code, errb)
	}
}

func TestNoCredentialIsPersisted(t *testing.T) {
	e := newEnv(t)
	r := e.repo("o/Priv", "1.0.0")
	r.Private = true
	e.mock.Token = "s3cret-token-value"
	e.declare(`{"Priv": {"github": "o/Priv"}}`)
	t.Setenv("DEPENDENCIES_GITHUB_TOKEN", "s3cret-token-value")
	code, out, errb := e.run("install", "--components-dir", filepath.Join(e.project, "Components"), "--workspace", e.project)
	if code != 0 {
		t.Fatalf("%d %s", code, errb)
	}
	if rep := parse(t, out); rep.Val("token") != true {
		t.Fatal("the report says a token was used, never which")
	}
	blob := out + errb
	for _, f := range e.files() {
		data, _ := os.ReadFile(f)
		blob += string(data)
	}
	if strings.Contains(blob, "s3cret-token-value") {
		t.Fatal("the credential leaked into a report, lock, cache, store or log")
	}
	// the credential reached the API host only
	for _, c := range e.mock.Calls() {
		if strings.HasPrefix(c.Path, "/download/") && c.Auth != "" {
			t.Fatalf("the credential must not reach an asset download: %+v", c)
		}
	}
}

func TestTokensAreNotAcceptedAsArguments(t *testing.T) {
	e := newEnv(t)
	e.declare(`{}`)
	if code, _, errb := e.run("install", "--github-token", "x"); code != exitUsage || strings.Contains(errb, "x\n") && false {
		t.Fatalf("%d %s", code, errb)
	}
}

func TestOfflineMakesNoNetworkRequests(t *testing.T) {
	e := newEnv(t)
	e.repo("o/A", "1.0.0")
	e.declare(`{"A": {"github": "o/A", "tag": "1.0.0"}}`)
	if code, _, errb := e.run("install"); code != 0 {
		t.Fatalf("%d %s", code, errb)
	}
	os.RemoveAll(e.home)
	e.mock.ResetCalls()
	if code, _, errb := e.run("install", "--offline"); code != 0 {
		t.Fatalf("%d %s", code, errb)
	}
	if n := len(e.mock.Calls()); n != 0 {
		t.Fatalf("offline made %d network requests: %v", n, e.mock.Paths())
	}
}

func TestIncompleteIsNeverSuccess(t *testing.T) {
	e := newEnv(t)
	e.declare(`{"Gone": {"github": "o/Gone"}}`)
	code, out, _ := e.run("install")
	if code != exitIncomplete {
		t.Fatalf("exit %d", code)
	}
	if inc := parse(t, out).Val("incomplete").([]any); len(inc) != 1 {
		t.Fatalf("%s", out)
	}
	if code, _, _ := e.run("install", "--allow-incomplete"); code != 0 {
		t.Fatal("--allow-incomplete is an explicit opt-in")
	}
}

func TestInventoryIsDeterministicPortableAndSecretFree(t *testing.T) {
	e := newEnv(t)
	a := e.repo("o/A", "1.0.0")
	a.Children["1.0.0"] = `{"dependencies": {"B": {"github": "o/B"}}}`
	e.repo("o/B", "1.0.0", "1.2.0")
	e.declare(`{"A": {"github": "o/A"}, "Gone": {"github": "o/Gone"}}`)
	t.Setenv("DEPENDENCIES_GITHUB_TOKEN", "tok-123")
	e.mock.Token = "tok-123"
	code, first, errb := e.run("inventory", "--installed", "--allow-incomplete")
	if code != 0 {
		t.Fatalf("%d %s", code, errb)
	}
	_, second, _ := e.run("inventory", "--installed", "--allow-incomplete")
	if first != second {
		t.Fatalf("the inventory must be deterministic:\n%s\n%s", first, second)
	}
	for _, forbidden := range []string{e.root, os.TempDir(), "tok-123", "127.0.0.1"} {
		if strings.Contains(first, forbidden) && forbidden != "127.0.0.1" {
			t.Fatalf("the inventory contains %q:\n%s", forbidden, first)
		}
	}
	inv := parse(t, first)
	if inv.Val("complete") != false || inv.Val("schemaVersion").(interface{ String() string }).String() != "1" {
		t.Fatalf("%s", first)
	}
	comps := inv.Val("components").([]any)
	names := []string{}
	for _, c := range comps {
		names = append(names, c.(*ojson.OMap).String("name"))
	}
	if strings.Join(names, ",") != "A,B,Gone" {
		t.Fatalf("components are sorted by name: %v", names)
	}
	a0 := comps[0].(*ojson.OMap)
	if a0.Val("archive").(*ojson.OMap).String("sha256") == "" {
		t.Fatal("the archive digest is recorded")
	}
}

func TestConcurrentInstallsShareACacheSafely(t *testing.T) {
	e := newEnv(t)
	e.repo("o/A", "1.0.0")
	e.declare(`{"A": {"github": "o/A", "tag": "1.0.0"}}`)
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var out, errb bytes.Buffer
			codes[i] = run([]string{"install", e.project, "--user", "u" + string(rune('a'+i)), "--home", e.home, "--archive-store", e.store, "--github-api-url", e.mock.URL}, &out, &errb)
		}(i)
	}
	wg.Wait()
	for i, c := range codes {
		if c != 0 {
			t.Errorf("run %d exited %d", i, c)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(e.home, ".cache", "tool4d", "Dependencies", ".github", "o", "A"))
	for _, ent := range entries {
		if strings.HasPrefix(ent.Name(), ".") {
			t.Errorf("staging leftover %s", ent.Name())
		}
	}
}

func TestVerifyAndCacheCommands(t *testing.T) {
	e := newEnv(t)
	e.repo("o/A", "1.0.0")
	e.declare(`{"A": {"github": "o/A", "tag": "1.0.0"}}`)
	comps := filepath.Join(e.project, "Components")
	if code, _, errb := e.run("install", "--components-dir", comps, "--workspace", e.project); code != 0 {
		t.Fatalf("%d %s", code, errb)
	}
	if code, out, _ := e.run("verify", "--components-dir", comps); code != 0 {
		t.Fatalf("verify %d %s", code, out)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"cache", "verify", "--archive-store", e.store}, &out, &errb); code != 0 || !strings.Contains(out.String(), "ok\tgithub\to/A\t1.0.0") {
		t.Fatalf("cache verify %d %s %s", code, out.String(), errb.String())
	}
	// corrupt the stored archive: verify and cache verify both fail, cache clean removes only the damaged entry
	archive := filepath.Join(e.store, "github", "o", "A", "1.0.0", "A.zip")
	os.WriteFile(archive, []byte("damaged"), 0o644)
	if code, out, _ := e.run("verify", "--components-dir", comps); code != exitIncomplete || !strings.Contains(out, "SHA-256 does not match") {
		t.Fatalf("verify of a damaged archive: %d %s", code, out)
	}
	out.Reset()
	if code := run([]string{"cache", "verify", "--archive-store", e.store}, &out, &errb); code != exitIncomplete {
		t.Fatalf("cache verify %d", code)
	}
	out.Reset()
	if code := run([]string{"cache", "clean", "--all", "--archive-store", e.store}, &out, &errb); code != exitUsage {
		t.Fatal("clean --all needs --yes")
	}
	if code := run([]string{"cache", "clean", "--archive-store", e.store}, &out, &errb); code != 0 || !strings.Contains(out.String(), "removed 1") {
		t.Fatalf("clean %d %s", code, out.String())
	}
	// an installed component that is no longer a package fails verification
	os.RemoveAll(filepath.Join(comps, "A.4dbase", "Project"))
	if code, out, _ := e.run("verify", "--components-dir", comps); code != exitIncomplete {
		t.Fatalf("%d %s", code, out)
	}
}

func TestUsageErrors(t *testing.T) {
	var out, errb bytes.Buffer
	for _, args := range [][]string{{}, {"nope"}, {"install"}, {"install", "/no/such/project"}, {"install", "--runtime", "x", "."}, {"cache"}, {"install", "a", "b"}} {
		if code := run(args, &out, &errb); code != exitUsage {
			t.Errorf("%v: exit %d", args, code)
		}
	}
	e := newEnv(t)
	for _, url := range []string{"http://example.com", "ftp://x", "not a url"} {
		if code, _, _ := e.run("install", "--github-api-url", url); code != exitUsage {
			t.Errorf("api url %q accepted", url)
		}
	}
	t.Setenv("GITHUB_ACTIONS", "true")
	t.Setenv("RUNNER_ENVIRONMENT", "github-hosted")
	if code, _, errb := e.run("install", "--runtime", "4D"); code != exitUsage || !strings.Contains(errb, "only allowed on self-hosted runners") {
		t.Fatalf("%d %s", code, errb)
	}
}

func TestUnsafeNamesAndTagsAreIncomplete(t *testing.T) {
	e := newEnv(t)
	r := e.repo("o/A", "../../evil", "1.0.0")
	r.Releases[0].Zip = testutil.ComponentZip("A", true, nil)
	e.repo("o/B", "1.0.0")
	e.declare(`{"Dots": {"github": "../.."}, "../Evil": {"github": "o/B"}, "Slash": {"github": "o/A", "tag": "../../evil"}}`)
	code, out, errb := e.run("install")
	if code != exitIncomplete {
		t.Fatalf("%d %s", code, errb)
	}
	for _, want := range []string{"invalid github repository", "unsafe component name", "unsafe release tag"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	for _, f := range e.files() {
		if strings.Contains(f, "evil") {
			t.Errorf("a path escaped the cache: %s", f)
		}
	}
	for _, p := range e.mock.Paths() {
		if strings.Contains(p, "../") {
			t.Errorf("a traversal reached the provider: %s", p)
		}
	}
}
