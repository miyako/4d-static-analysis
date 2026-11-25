// Package fsx holds the filesystem primitives: atomic writes, cross-process locks, safe ZIP extraction, atomic
// directory installation and the per-platform cache locations.
package fsx

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/miyako/4d-static-analysis/components/internal/ojson"
	"github.com/miyako/4d-static-analysis/components/internal/version"
)

const (
	MaxArchive   = 1 << 30
	MaxExtracted = int64(2) << 30
	MaxEntries   = 50000
)

func failf(format string, args ...any) error {
	return &version.Failure{Msg: fmt.Sprintf(format, args...)}
}

func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func SHA256Bytes(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// WriteJSON writes v atomically (temporary file in the same folder, then rename).
func WriteJSON(path string, v any, indent string) error {
	data, err := ojson.Marshal(v, indent)
	if err != nil {
		return err
	}
	return WriteFileAtomic(path, data, 0o644)
}

func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	_ = os.Chmod(name, mode)
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// FileLock serialises processes that share a cache. The lock is an exclusively created file; one older than
// `stale` is taken over. The returned function releases it.
func FileLock(path string, timeout, stale time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d", os.Getpid())
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if st, serr := os.Stat(path); serr == nil && time.Since(st.ModTime()) > stale {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, failf("timed out waiting for lock %s", path)
		}
		time.Sleep(200*time.Millisecond + time.Duration(time.Now().UnixNano()%300)*time.Millisecond)
	}
}

func globSuffix(dir, suffix string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		n := e.Name()
		if runtime.GOOS == "windows" {
			n, suffix = strings.ToLower(n), strings.ToLower(suffix)
		}
		if strings.HasSuffix(n, suffix) && n != suffix {
			return true
		}
	}
	return false
}

// PackageForm recognises a 4D component package: "compiled" (a .4DZ, flat or under Contents/), "interpreted"
// (Project/*.4DProject) or "" for neither.
func PackageForm(root string) string {
	if globSuffix(filepath.Join(root, "Contents"), ".4DZ") || globSuffix(root, ".4DZ") {
		return "compiled"
	}
	if st, err := os.Stat(filepath.Join(root, "Project")); err == nil && st.IsDir() && globSuffix(filepath.Join(root, "Project"), ".4DProject") {
		return "interpreted"
	}
	return ""
}

var driveRe = regexp.MustCompile(`^[A-Za-z]:`)

func pyRepr(s string) string { return "'" + strings.ReplaceAll(s, "'", `\'`) + "'" }

// SafeExtract extracts a component archive into dest, treating it as untrusted: it rejects absolute paths,
// "..", drive letters, backslashes, symbolic links and other special entries, duplicate entries, and archives
// beyond the entry and size limits (checked against the bytes actually written, not the declared sizes). One
// `<name>.4dbase` wrapper folder is stripped, as 4D stores unwrapped content. Every failure is a Failure, so
// the component is reported incomplete; a partial extraction is never complete.
func SafeExtract(archive, dest string) (string, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return "", failf("not a readable ZIP archive: %v", err)
	}
	defer zr.Close()
	if len(zr.File) > MaxEntries {
		return "", failf("archive exceeds extraction limits")
	}
	var declared uint64
	for _, f := range zr.File {
		declared += f.UncompressedSize64
	}
	if declared > uint64(MaxExtracted) {
		return "", failf("archive exceeds extraction limits")
	}
	budget := MaxExtracted
	seen := map[string]bool{}
	destClean := filepath.Clean(dest)
	for _, f := range zr.File {
		name := f.Name
		base := name[strings.LastIndex(name, "/")+1:]
		if strings.HasPrefix(name, "__MACOSX/") || base == ".DS_Store" {
			continue
		}
		unix := (f.ExternalAttrs >> 16) & 0o170000
		bad := strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) || strings.Contains(name, `\`) ||
			strings.ContainsRune(name, 0) || driveRe.MatchString(name) || name == "" ||
			unix == 0o120000 || (unix != 0 && unix != 0o100000 && unix != 0o040000) ||
			(runtime.GOOS == "windows" && strings.Contains(name, ":"))
		for _, part := range strings.Split(name, "/") {
			if part == ".." {
				bad = true
			}
		}
		if bad {
			return "", failf("unsafe archive entry %s", pyRepr(name))
		}
		key := strings.TrimSuffix(name, "/")
		if seen[key] {
			return "", failf("unsafe archive entry %s: duplicate entry", pyRepr(name))
		}
		seen[key] = true
		target := filepath.Join(dest, filepath.FromSlash(name))
		if rel, err := filepath.Rel(destClean, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", failf("unsafe archive entry %s", pyRepr(name))
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return "", err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		src, err := f.Open()
		if err != nil {
			return "", failf("corrupt archive entry %s: %v", pyRepr(name), err)
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			src.Close()
			return "", failf("unsafe archive entry %s: %v", pyRepr(name), err)
		}
		n, err := io.Copy(out, io.LimitReader(src, budget+1))
		src.Close()
		cerr := out.Close()
		budget -= n
		if budget < 0 {
			return "", failf("archive exceeds extraction limits")
		}
		if err != nil {
			return "", failf("corrupt archive entry %s: %v", pyRepr(name), err)
		}
		if cerr != nil {
			return "", cerr
		}
		if mode := (f.ExternalAttrs >> 16) & 0o777; mode != 0 && runtime.GOOS != "windows" {
			_ = os.Chmod(target, os.FileMode(mode|0o600))
		}
	}
	kids, err := os.ReadDir(dest)
	if err != nil {
		return "", err
	}
	root := dest
	var filtered []os.DirEntry
	for _, k := range kids {
		if k.Name() != "__MACOSX" && k.Name() != ".DS_Store" {
			filtered = append(filtered, k)
		}
	}
	if len(filtered) == 1 && filtered[0].IsDir() && strings.EqualFold(filepath.Ext(filtered[0].Name()), ".4dbase") {
		root = filepath.Join(dest, filtered[0].Name())
	}
	if PackageForm(root) == "" {
		return "", failf("archive is not a recognised 4D component package")
	}
	return root, nil
}

// MoveTree renames source to target, copying when they are on different filesystems.
func MoveTree(source, target string) error {
	if err := os.Rename(source, target); err == nil {
		return nil
	}
	if err := CopyTree(source, target); err != nil {
		return err
	}
	return os.RemoveAll(source)
}

// InstallTree atomically moves source to target. It reports false when target already existed and was kept
// (another process won); with replace, an existing target is swapped out first.
func InstallTree(source, target string, replace bool) (bool, error) {
	stage := filepath.Join(filepath.Dir(target), fmt.Sprintf(".%s.%d.partial", filepath.Base(target), os.Getpid()))
	os.RemoveAll(stage)
	if err := MoveTree(source, stage); err != nil {
		return false, err
	}
	if _, err := os.Stat(target); err == nil && replace {
		trash := filepath.Join(filepath.Dir(target), fmt.Sprintf(".%s.%d.old", filepath.Base(target), os.Getpid()))
		if err := os.Rename(target, trash); err != nil {
			os.RemoveAll(stage)
			return false, err
		}
		os.RemoveAll(trash)
	}
	if err := os.Rename(stage, target); err != nil {
		os.RemoveAll(stage)
		if st, serr := os.Stat(target); serr == nil && st.IsDir() {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// CopyTree copies a directory tree. It never follows or recreates symbolic links: a link inside the tree is an
// error, so that a copy cannot be redirected outside the intended folder.
func CopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return failf("symbolic link %s is not copied", rel)
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case info.Mode().IsRegular():
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()|0o600)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				out.Close()
				return err
			}
			return out.Close()
		}
		return failf("special file %s is not copied", rel)
	})
}

// PlatformName is macos, windows or linux.
func PlatformName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "windows":
		return "windows"
	}
	return "linux"
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// UserCache is the per-user cache root: ~/Library/Caches, %LOCALAPPDATA% or $XDG_CACHE_HOME. With an isolated
// `home` the cache lives inside it, where tool4d looks for it.
func UserCache(home string) string {
	switch PlatformName() {
	case "macos":
		if home == "" {
			home = homeDir()
		}
		return filepath.Join(home, "Library", "Caches")
	case "windows":
		if home != "" {
			return filepath.Join(home, "AppData", "Local")
		}
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return v
		}
		return filepath.Join(homeDir(), "AppData", "Local")
	}
	if home != "" {
		return filepath.Join(home, ".cache")
	}
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return v
	}
	return filepath.Join(homeDir(), ".cache")
}

// IsolationEnv is the environment that points tool4d at an isolated profile.
func IsolationEnv(home string) (map[string]string, error) {
	switch PlatformName() {
	case "macos":
		return map[string]string{"HOME": home, "CFFIXED_USER_HOME": home}, nil
	case "windows":
		local, roaming := filepath.Join(home, "AppData", "Local"), filepath.Join(home, "AppData", "Roaming")
		for _, d := range []string{local, roaming} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return nil, err
			}
		}
		return map[string]string{"USERPROFILE": home, "HOME": home, "LOCALAPPDATA": local, "APPDATA": roaming}, nil
	}
	return map[string]string{"HOME": home, "XDG_CACHE_HOME": filepath.Join(home, ".cache")}, nil
}

// NativePath writes a folder path as 4D writes it in the lock file: HFS ("Volume:a:b:") on macOS, otherwise the
// native path with a trailing separator.
func NativePath(path string) string {
	if PlatformName() != "macos" {
		return path + string(os.PathSeparator)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		real = path
	}
	volume, rest := "", real
	if strings.HasPrefix(real, "/Volumes/") {
		v, r, _ := strings.Cut(strings.TrimPrefix(real, "/Volumes/"), "/")
		volume, rest = v, r
	}
	if volume == "" {
		if entries, err := os.ReadDir("/Volumes"); err == nil {
			for _, e := range entries {
				if t, err := filepath.EvalSymlinks(filepath.Join("/Volumes", e.Name())); err == nil && t == "/" {
					volume = e.Name()
					break
				}
			}
		}
	}
	if volume == "" {
		volume = "Macintosh HD"
	}
	parts := []string{volume}
	for _, p := range strings.Split(rest, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ":") + ":"
}
