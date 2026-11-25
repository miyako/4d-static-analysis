package resolver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/miyako/4d-static-analysis/components/internal/fsx"
	"github.com/miyako/4d-static-analysis/components/internal/ojson"
	"github.com/miyako/4d-static-analysis/components/internal/provider"
	"github.com/miyako/4d-static-analysis/components/internal/version"
)

const (
	lockTimeout = 600 * time.Second
	lockStale   = 1800 * time.Second
)

func ifNil(v any) any { return v }

// metaValue returns the value or nil; the cache metadata writes null for what a release does not provide.
func metaKeys(meta *ojson.OMap, keys ...string) *ojson.OMap {
	out := ojson.New()
	for _, k := range keys {
		out.Set(k, meta.Val(k))
	}
	return out
}

func (m *Manager) updateFromMeta(e, meta *ojson.OMap) {
	for _, k := range []string{"htmlURL", "archiveURL", "archiveSize", "dependencies"} {
		e.Set(k, meta.Val(k))
	}
}

// asset selects the release asset: a GitHub component is `<repo>.zip`; a GitLab release's first link is used
// whatever it is called (observed case K1t: the first one the API lists).
func asset(rel *provider.Release, src *Source) (*provider.Asset, error) {
	if src.gitlab() {
		if len(rel.Assets) == 0 {
			return nil, failf("cannot determine the archive URL of GitLab release %s of %s: the release has no asset link", rel.Tag, src.Repo)
		}
		return rel.Assets[0], nil
	}
	name := src.Repo[strings.LastIndex(src.Repo, "/")+1:] + ".zip"
	for _, a := range rel.Assets {
		if a.Name == name {
			return a, nil
		}
	}
	return nil, failf("release %s of %s has no %s asset", rel.Tag, src.Repo, name)
}

// checkDigest verifies a download against the release's digest and size when it provides them, and returns the
// SHA-256 of what was downloaded.
func checkDigest(data []byte, a *provider.Asset) (string, error) {
	actual := fsx.SHA256Bytes(data)
	if a.Digest != "" {
		algo, want, _ := strings.Cut(a.Digest, ":")
		if !strings.EqualFold(algo, "sha256") {
			// An integrity claim that cannot be checked is not silently ignored.
			return "", failf("%s: unsupported release asset digest algorithm %s", a.Name, ojson.Repr(algo))
		}
		if strings.ToLower(want) != actual {
			return "", failf("%s: sha256 does not match the release asset digest", a.Name)
		}
	}
	if a.Size != nil && *a.Size != int64(len(data)) {
		return "", failf("%s: size does not match the release asset", a.Name)
	}
	return actual, nil
}

func (m *Manager) newMeta(rel *provider.Release, a *provider.Asset, src *Source, size int) *ojson.OMap {
	meta := ojson.New().Set("tag", rel.Tag).Set("htmlURL", rel.HTMLURL).Set("archiveURL", src.Client.ArchiveURL(a))
	if a.Size != nil {
		meta.Set("archiveSize", *a.Size)
	} else {
		meta.Set("archiveSize", int64(size))
	}
	meta.Set("asset", a.Name).Set("assetId", a.ID)
	if a.Digest != "" {
		meta.Set("digest", a.Digest)
	} else {
		meta.Set("digest", nil)
	}
	if src.gitlab() {
		meta.Set("gitlab", src.Repo)
		if src.Host != "" {
			meta.Set("host", src.Host)
		} else {
			meta.Set("host", nil)
		}
	} else {
		meta.Set("github", src.Repo)
	}
	return meta
}

func readJSONObject(path string) *ojson.OMap {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	v, err := ojson.Parse(ojson.StripBOM(data))
	if err != nil {
		return nil
	}
	o, _ := v.(*ojson.OMap)
	return o
}

// materialise installs the release in the cache layout 4D, 4D Server and tool4d read, and records where.
func (m *Manager) materialise(e *ojson.OMap, src *Source, tag string, rel *provider.Release) error {
	if !safeSegment(tag) {
		return failf("unsafe release tag %s", ojson.Repr(tag))
	}
	repoFolder := src.cacheFolder(m.depsFolder)
	target := filepath.Join(repoFolder, tag)
	metaPath := filepath.Join(repoFolder, tag+".json")
	if m.opts.PlanOnly {
		return m.plan(e, src, tag, rel)
	}
	if m.store == "" && isDir(target) && isFile(metaPath) {
		meta := readJSONObject(metaPath)
		if meta == nil {
			return usage("cannot read %s", metaPath)
		}
		m.updateFromMeta(e, meta)
		if fsx.PackageForm(target) == "" {
			// 4D keeps the metadata but neither loads nor repairs a damaged cache folder.
			return failf("cached %s@%s is not a loadable package and 4D does not repair it; delete %s to re-fetch", src.Repo, tag, target)
		}
		e.Set("source", "cache").Set("reuse", UnverifiedReuse).Set("path", target)
		return nil
	}
	if err := os.MkdirAll(repoFolder, 0o755); err != nil {
		return err
	}
	source := "download"
	var archive string
	var meta *ojson.OMap
	var err error
	if m.store != "" {
		var unlock func()
		archive, meta, source, unlock, err = m.archived(src, tag, rel)
		if err != nil {
			return err
		}
		defer unlock() // held until the verified archive has been extracted
	}
	tmp, err := os.MkdirTemp(repoFolder, ".fetch-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if archive == "" {
		if rel == nil {
			if rel, err = src.Client.Release(src.Repo, version.Decl{Kind: version.KindTag, Tag: tag}); err != nil {
				return err
			}
		}
		a, err := asset(rel, src)
		if err != nil {
			return err
		}
		data, err := src.Client.Download(a)
		if err != nil {
			return err
		}
		if _, err := checkDigest(data, a); err != nil {
			return err
		}
		archive = filepath.Join(tmp, "archive.zip")
		if err := os.WriteFile(archive, data, 0o600); err != nil {
			return err
		}
		meta = m.newMeta(rel, a, src, len(data))
		meta.Set("sha256", fsx.SHA256Bytes(data))
		deps, err := src.Client.ChildDependencies(src.Repo, tag)
		if err != nil {
			return err
		}
		if deps == nil {
			meta.Set("dependencies", nil)
		} else {
			meta.Set("dependencies", deps)
		}
	}
	root, err := fsx.SafeExtract(archive, filepath.Join(tmp, "x"))
	if err != nil {
		return err
	}
	release, err := fsx.FileLock(filepath.Join(repoFolder, "."+tag+".lock"), lockTimeout, lockStale)
	if err != nil {
		return err
	}
	installed, err := fsx.InstallTree(root, target, m.store != "")
	if err == nil && !installed && fsx.PackageForm(target) == "" {
		// Another folder with this tag is in the way and is not a package: 4D would not load or repair it.
		release()
		return failf("cached %s@%s is not a loadable package and 4D does not repair it; delete %s to re-fetch", src.Repo, tag, target)
	}
	if err == nil && (installed || !isFile(metaPath)) {
		keys := []string{"github", "tag", "htmlURL", "archiveURL", "archiveSize", "dependencies"}
		if src.gitlab() {
			keys = []string{"tag", "htmlURL", "archiveURL", "archiveSize", "dependencies", "gitlab", "host"}
		}
		err = fsx.WriteJSON(metaPath, metaKeys(meta, keys...), "\t")
	}
	release()
	if err != nil {
		return err
	}
	reuse := "verified archive"
	if m.store == "" {
		reuse = "downloaded"
		if !installed {
			reuse, source = UnverifiedReuse, "cache"
		}
	}
	e.Set("source", source).Set("reuse", reuse).Set("path", target)
	m.updateFromMeta(e, meta)
	if sum := meta.Val("sha256"); sum != nil {
		e.Set("archiveSha256", sum)
	}
	return nil
}

// plan records what a run would install without touching the cache (`resolve` command): the release metadata
// comes from the archive store when it holds the tag, otherwise from the provider.
func (m *Manager) plan(e *ojson.OMap, src *Source, tag string, rel *provider.Release) error {
	if m.store != "" {
		if stored := readJSONObject(filepath.Join(src.storeFolder(m.store), tag, "meta.json")); stored != nil && ojson.Truthy(stored.Val("asset")) {
			m.updateFromMeta(e, stored)
			if sum := stored.Val("sha256"); sum != nil {
				e.Set("archiveSha256", sum)
			}
			e.Set("source", "planned").Set("reuse", "not installed")
			return nil
		}
	}
	if rel == nil {
		var err error
		if rel, err = src.Client.Release(src.Repo, version.Decl{Kind: version.KindTag, Tag: tag}); err != nil {
			return err
		}
	}
	a, err := asset(rel, src)
	if err != nil {
		return err
	}
	meta := m.newMeta(rel, a, src, 0)
	deps, err := src.Client.ChildDependencies(src.Repo, tag)
	if err != nil {
		return err
	}
	if deps == nil {
		meta.Set("dependencies", nil)
	} else {
		meta.Set("dependencies", deps)
	}
	m.updateFromMeta(e, meta)
	e.Set("source", "planned").Set("reuse", "not installed")
	return nil
}

// archived returns the archive from the verified store, downloading on a miss. A stored archive is re-hashed on
// every use and checked against the current release digest (online) or the digest recorded when it was stored
// (offline). Stored metadata is never trusted on its own: it must match the bytes.
func (m *Manager) archived(src *Source, tag string, rel *provider.Release) (string, *ojson.OMap, string, func(), error) {
	client := src.Client
	folder := filepath.Join(src.storeFolder(m.store), tag)
	metaPath := filepath.Join(folder, "meta.json")
	unlock, err := fsx.FileLock(filepath.Join(src.storeFolder(m.store), "."+tag+".lock"), lockTimeout, lockStale)
	if err != nil {
		return "", nil, "", nil, err
	}
	ok := false
	defer func() {
		if !ok {
			unlock()
		}
	}()
	stored := readJSONObject(metaPath)
	var current *provider.Asset
	var lookupErr error
	if !client.Offline() {
		if rel == nil {
			rel, lookupErr = client.Release(src.Repo, version.Decl{Kind: version.KindTag, Tag: tag})
		}
		if lookupErr == nil {
			current, lookupErr = asset(rel, src)
		}
		var auth *provider.AuthFailure
		if errors.As(lookupErr, &auth) {
			return "", nil, "", nil, lookupErr // a rejected token is not an outage: no fallback, as 4D does not retry
		}
		if lookupErr != nil && !isFailure(lookupErr) {
			return "", nil, "", nil, lookupErr
		}
	}
	if stored != nil && ojson.Truthy(stored.Val("asset")) {
		file := ojson.Str(stored.Val("asset"))
		if f, ok := stored.Get("file"); ok {
			file = ojson.Str(f)
		}
		if !safeSegment(file) {
			m.warn("%s@%s: stored archive has an unsafe file name; discarded", src.Repo, tag)
		} else {
			archive := filepath.Join(folder, file)
			expected := ""
			if current != nil && current.Digest != "" {
				expected = current.Digest
			} else if d, isStr := stored.Val("digest").(string); isStr {
				expected = d
			}
			if isFile(archive) {
				actual, err := fsx.SHA256File(archive)
				if err == nil && ojson.Equal(stored.Val("sha256"), actual) && (expected == "" || strings.EqualFold(expected, "sha256:"+actual)) {
					if lookupErr != nil {
						m.warn("%s@%s: release lookup failed (%s); using the archive verified against its recorded digest", src.Repo, tag, lookupErr)
					}
					ok = true
					return archive, stored, "archive", unlock, nil
				}
				m.warn("%s@%s: stored archive failed verification; discarded", src.Repo, tag)
			}
		}
	}
	if current == nil {
		if lookupErr != nil {
			return "", nil, "", nil, lookupErr
		}
		return "", nil, "", nil, failf("%s@%s: offline and no verified archive is stored", src.Repo, tag)
	}
	data, err := client.Download(current)
	if err != nil {
		return "", nil, "", nil, err
	}
	meta := m.newMeta(rel, current, src, len(data))
	sum, err := checkDigest(data, current)
	if err != nil {
		return "", nil, "", nil, err
	}
	meta.Set("sha256", sum)
	deps, err := client.ChildDependencies(src.Repo, tag)
	if err != nil {
		return "", nil, "", nil, err
	}
	if deps == nil {
		meta.Set("dependencies", nil)
	} else {
		meta.Set("dependencies", deps)
	}
	// A GitLab link name is free text; store its archive under a fixed, safe file name.
	file := current.Name
	if src.gitlab() {
		file = "archive.zip"
	}
	if !safeSegment(file) {
		return "", nil, "", nil, failf("unsafe asset name %s", ojson.Repr(file))
	}
	meta.Set("file", file)
	if err := fsx.WriteFileAtomic(filepath.Join(folder, file), data, 0o600); err != nil {
		return "", nil, "", nil, err
	}
	if err := fsx.WriteJSON(metaPath, meta, "\t"); err != nil {
		return "", nil, "", nil, err
	}
	ok = true
	return filepath.Join(folder, file), meta, "download", unlock, nil
}

var _ = fmt.Sprintf
var _ = ifNil
