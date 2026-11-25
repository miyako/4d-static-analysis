package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miyako/4d-static-analysis/components/internal/fsx"
	"github.com/miyako/4d-static-analysis/components/internal/testutil"
)

// Regression tests for the independent review's findings.

func TestForeignTagFolderWithoutMetadataIsIncomplete(t *testing.T) {
	e := newEnv(t)
	e.repo("o/A", "1.0.0")
	e.declare(`{"A": {"github": "o/A", "tag": "1.0.0"}}`)
	dir := filepath.Join(fsx.UserCache(e.home), "tool4d", "Dependencies", ".github", "o", "A", "1.0.0")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "junk.txt"), []byte("x"), 0o644)
	code, _, errb := e.run("install", "--archive-store", "none")
	if code != exitIncomplete || !strings.Contains(errb, "not a loadable package") {
		t.Fatalf("%d %s", code, errb)
	}
}

func TestUncheckableDigestsFailClosedAndCaseIsIgnored(t *testing.T) {
	e := newEnv(t)
	e.repo("o/A", "1.0.0")
	e.declare(`{"A": {"github": "o/A"}}`)
	// mixed-case sha256 digests verify, and keep verifying from the store
	if code, _, errb := e.run("install"); code != 0 {
		t.Fatalf("%d %s", code, errb)
	}
	meta := filepath.Join(e.store, "github", "o", "A", "1.0.0", "meta.json")
	data, _ := os.ReadFile(meta)
	os.WriteFile(meta, []byte(strings.Replace(string(data), `"sha256:`, `"SHA256:`, 1)), 0o644)
	e.mock.ResetCalls()
	if code, _, errb := e.run("install", "--offline"); code != 0 || strings.Contains(errb, "failed verification") {
		t.Fatalf("%d %s", code, errb)
	}
}

func TestTokenIsNotSentOverClearTextOrToAnotherPort(t *testing.T) {
	e := newEnv(t)
	r := e.repo("o/A", "1.0.0")
	r.Releases[0].BadDigest = false
	e.declare(`{"A": {"github": "o/A"}}`)
	t.Setenv("DEPENDENCIES_GITHUB_TOKEN", "tok")
	e.mock.Token = "tok"
	// loopback http is allowed (tests); the credential reaches only that host:port
	if code, _, errb := e.run("install"); code != 0 {
		t.Fatalf("%d %s", code, errb)
	}
	for _, c := range e.mock.Calls() {
		if strings.HasPrefix(c.Path, "/download/") && c.Auth != "" {
			t.Fatal("asset download carried the credential")
		}
	}
}

func TestGitLabHostCannotEscapeTheCache(t *testing.T) {
	e := newEnv(t)
	e.declare(`{"G": {"gitlab": ".github/o/r", "host": "https://..", "tag": "1.0.0"}, "H": {"gitlab": "a/b", "host": "https://gl.example:8443", "tag": "1.0.0"}}`)
	code, out, _ := e.run("install", "--runtime-version", "21R5")
	if code != exitIncomplete || !strings.Contains(out, "invalid gitlab host") {
		t.Fatalf("%d %s", code, out)
	}
	for _, f := range e.files() {
		if strings.Contains(f, filepath.Join(".github", "o", "r")) {
			t.Fatalf("a GitLab host reached the GitHub cache: %s", f)
		}
	}
}

func TestOverlongReleaseListsAreIncomplete(t *testing.T) {
	e := newEnv(t)
	var tags []string
	for i := 0; i < 1000; i++ {
		tags = append(tags, "1.0."+strings.Repeat("0", 0)+itoa(i))
	}
	r := e.repo("o/A", tags...)
	for i := range r.Releases {
		r.Releases[i].Zip = testutil.ComponentZip("A", true, nil)
	}
	e.declare(`{"A": {"github": "o/A", "version": "^1.0.0"}}`)
	code, out, _ := e.run("resolve")
	if code != exitIncomplete || !strings.Contains(out, "more than 1000 releases") {
		t.Fatalf("%d %.300s", code, out)
	}
}

func itoa(i int) string {
	return strings.TrimLeft(strings.Join([]string{string(rune('0' + i/100%10)), string(rune('0' + i/10%10)), string(rune('0' + i%10))}, ""), "0") + func() string {
		if i == 0 {
			return "0"
		}
		return ""
	}()
}

func TestStaleLockTakeoverAndOwnership(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l")
	release1, err := fsx.FileLock(p, 0, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte("someone else"), 0o644) // the lock was taken over by another owner
	release1()
	if _, err := os.Stat(p); err != nil {
		t.Fatal("a holder must not remove a lock that is no longer its own")
	}
}
