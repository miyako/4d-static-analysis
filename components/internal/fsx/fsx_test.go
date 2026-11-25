package fsx

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/miyako/4d-static-analysis/components/internal/testutil"
)

func writeZip(t *testing.T, entries map[string]string, modes map[string]uint32) string {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	var names []string
	for n := range entries {
		names = append(names, n)
	}
	for _, n := range names {
		h := &zip.FileHeader{Name: n, Method: zip.Deflate}
		if m, ok := modes[n]; ok {
			h.SetMode(os.FileMode(m))
			h.ExternalAttrs = (m) << 16
		}
		w, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(entries[n]))
	}
	z.Close()
	path := filepath.Join(t.TempDir(), "a.zip")
	os.WriteFile(path, b.Bytes(), 0o644)
	return path
}

func TestExtractWrappedAndFlat(t *testing.T) {
	for _, wrapped := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), "a.zip")
		os.WriteFile(path, testutil.ComponentZip("C", wrapped, nil), 0o644)
		root, err := SafeExtract(path, filepath.Join(t.TempDir(), "x"))
		if err != nil {
			t.Fatal(err)
		}
		if PackageForm(root) != "interpreted" {
			t.Fatalf("wrapped=%v root=%s", wrapped, root)
		}
		if wrapped == strings.HasSuffix(root, ".4dbase") == false {
			t.Fatalf("wrapper handling wrong: %s", root)
		}
	}
}

func TestExtractRejectsUnsafe(t *testing.T) {
	good := map[string]string{"Project/C.4DProject": "{}"}
	cases := map[string]map[string]string{
		"traversal": {"../evil": "x"}, "nested traversal": {"a/../../evil": "x"}, "absolute": {"/etc/evil": "x"},
		"backslash": {`a\b`: "x"}, "drive": {"C:evil": "x"}, "backslash absolute": {`\evil`: "x"},
	}
	for name, extra := range cases {
		entries := map[string]string{}
		for k, v := range good {
			entries[k] = v
		}
		for k, v := range extra {
			entries[k] = v
		}
		_, err := SafeExtract(writeZip(t, entries, nil), filepath.Join(t.TempDir(), "x"))
		if err == nil || !strings.Contains(err.Error(), "unsafe archive entry") {
			t.Errorf("%s: %v", name, err)
		}
	}
	link := writeZip(t, map[string]string{"Project/C.4DProject": "{}", "l": "/etc/passwd"}, map[string]uint32{"l": 0o120777})
	if _, err := SafeExtract(link, filepath.Join(t.TempDir(), "x")); err == nil || !strings.Contains(err.Error(), "unsafe archive entry 'l'") {
		t.Errorf("symlink: %v", err)
	}
	dev := writeZip(t, map[string]string{"Project/C.4DProject": "{}", "d": ""}, map[string]uint32{"d": 0o020644})
	if _, err := SafeExtract(dev, filepath.Join(t.TempDir(), "x")); err == nil {
		t.Error("special entry must be rejected")
	}
}

func TestExtractRejectsDuplicatesCorruptAndNonPackages(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for i := 0; i < 2; i++ {
		w, _ := z.Create("Project/C.4DProject")
		w.Write([]byte("{}"))
	}
	z.Close()
	p := filepath.Join(t.TempDir(), "dup.zip")
	os.WriteFile(p, b.Bytes(), 0o644)
	if _, err := SafeExtract(p, filepath.Join(t.TempDir(), "x")); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.zip")
	os.WriteFile(bad, []byte("not a zip"), 0o644)
	if _, err := SafeExtract(bad, filepath.Join(t.TempDir(), "x")); err == nil {
		t.Error("corrupt ZIP must fail as a Failure, not crash")
	}
	if _, err := SafeExtract(writeZip(t, map[string]string{"readme.txt": "x"}, nil), filepath.Join(t.TempDir(), "x")); err == nil || !strings.Contains(err.Error(), "not a recognised 4D component package") {
		t.Errorf("non-package: %v", err)
	}
}

func TestPackageForms(t *testing.T) {
	d := t.TempDir()
	if PackageForm(d) != "" {
		t.Fatal("empty folder")
	}
	os.MkdirAll(filepath.Join(d, "Contents"), 0o755)
	os.WriteFile(filepath.Join(d, "Contents", "C.4DZ"), nil, 0o644)
	if PackageForm(d) != "compiled" {
		t.Fatal("new compiled form")
	}
	flat := t.TempDir()
	os.WriteFile(filepath.Join(flat, "C.4DZ"), nil, 0o644)
	if PackageForm(flat) != "compiled" {
		t.Fatal("old flat compiled form")
	}
}

func TestInstallTreeAtomicAndKeepsExisting(t *testing.T) {
	base := t.TempDir()
	mk := func(name, content string) string {
		p := filepath.Join(base, name)
		os.MkdirAll(p, 0o755)
		os.WriteFile(filepath.Join(p, "f"), []byte(content), 0o644)
		return p
	}
	target := filepath.Join(base, "target")
	if ok, err := InstallTree(mk("s1", "one"), target, false); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := InstallTree(mk("s2", "two"), target, false); ok || err != nil {
		t.Fatalf("an existing folder is kept: %v %v", ok, err)
	}
	if b, _ := os.ReadFile(filepath.Join(target, "f")); string(b) != "one" {
		t.Fatal("existing content must be unchanged")
	}
	if ok, err := InstallTree(mk("s3", "three"), target, true); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if b, _ := os.ReadFile(filepath.Join(target, "f")); string(b) != "three" {
		t.Fatal("replace")
	}
	entries, _ := os.ReadDir(base)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("staging leftover %s", e.Name())
		}
	}
}

func TestCopyTreeRefusesSymlinks(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "f"), []byte("x"), 0o644)
	if err := os.Symlink("/etc", filepath.Join(src, "escape")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := CopyTree(src, filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Fatal("a symlink must not be copied")
	}
}

func TestFileLockExcludes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "l")
	release, err := FileLock(p, 0, 1<<40)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FileLock(p, 0, 1<<40); err == nil {
		t.Fatal("a held lock must not be acquired twice")
	}
	release()
	if release2, err := FileLock(p, 0, 1<<40); err != nil {
		t.Fatal(err)
	} else {
		release2()
	}
}
