package resolver

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/miyako/4d-static-analysis/components/internal/fsx"
	"github.com/miyako/4d-static-analysis/components/internal/ojson"
)

func nativePath(e *ojson.OMap) string {
	if p := ojson.StrOrEmpty(e.Val("path")); p != "" {
		return fsx.NativePath(p)
	}
	return ""
}

func found(e *ojson.OMap) bool {
	return ojson.Truthy(e.Val("path")) && (!ojson.Truthy(e.Val("error")) || ojson.Truthy(e.Val("disabled")))
}

func loadedField(e *ojson.OMap) any {
	if ojson.Truthy(e.Val("disabled")) {
		return false
	}
	return nil
}

func addErrors(item, e *ojson.OMap, nested bool) {
	if ojson.Truthy(e.Val("errors")) {
		item.Set("errors", e.Val("errors"))
	} else if ojson.Truthy(e.Val("error")) {
		item.Set("errors", []any{ojson.New().Set("message", e.Val("error"))})
	}
}

// writeLock writes dependencies-lock.json in 4D's format. `loaded` is null: the manager cannot know whether the
// runtime loaded a component. GitHub entries also carry archiveSize and dependencies, and the dependencies nested
// in an entry are written as declared (4D sometimes expands them).
func (m *Manager) writeLock() error {
	lock := ojson.New()
	if m.versionCode != 0 {
		lock.Set("version", ojson.Int(m.versionCode))
	}
	deps := ojson.New()
	lock.Set("dependencies", deps)
	names := append([]string(nil), m.order...)
	sort.Strings(names)
	for _, name := range names {
		e := m.results[name]
		switch {
		case e.Val("source") == "local":
			deps.Set(name, m.localLockItem(e, false))
		case ojson.Truthy(e.Val("gitlab")):
			deps.Set(name, gitlabLockItem(e))
		case !ojson.Truthy(e.Val("github")):
			continue
		default:
			item := ojson.New().Set("github", e.Val("github"))
			for _, k := range []string{"version", "tag", "htmlURL", "archiveURL", "archiveSize"} {
				if v := e.Val(k); v != nil {
					item.Set(k, v)
				}
			}
			if ojson.Truthy(e.Val("error")) && !ojson.Truthy(e.Val("htmlURL")) {
				// Without release metadata 4D writes no tag and empty URLs.
				item.Delete("tag")
				item.Set("htmlURL", "").Set("archiveURL", "")
			}
			item.Set("path", nativePath(e))
			item.Set("dependencies", e.Val("dependencies"))
			item.Set("found", found(e))
			item.Set("loaded", loadedField(e))
			addErrors(item, e, false)
			deps.Set(name, item)
		}
	}
	if err := os.MkdirAll(filepath.Dir(m.lockPath), 0o755); err != nil {
		return err
	}
	return fsx.WriteJSON(m.lockPath, lock, "\t")
}

// gitlabLockItem is a GitLab entry as 4D 21R5 writes it: no archiveSize and no dependencies, `host` only when
// declared, and a relative htmlURL.
func gitlabLockItem(e *ojson.OMap) *ojson.OMap {
	item := ojson.New().Set("gitlab", e.Val("gitlab"))
	for _, k := range []string{"host", "version", "tag", "htmlURL", "archiveURL"} {
		if v := e.Val(k); v != nil {
			item.Set(k, v)
		}
	}
	item.Set("path", nativePath(e))
	item.Set("found", found(e))
	item.Set("loaded", loadedField(e))
	addErrors(item, e, false)
	return item
}

// localLockItem is a local component as 4D writes it. Its declarations are written as declared, except that a
// local one that declares nothing itself, or was not found, is replaced by its resolved path/loaded/found; one
// that declares dependencies keeps its declared path and gains them.
func (m *Manager) localLockItem(e *ojson.OMap, nested bool) *ojson.OMap {
	item := ojson.New().Set("path", nativePath(e))
	if children, ok := e.Val("dependencies").(*ojson.OMap); ok && !nested && children.Len() > 0 {
		out := ojson.New()
		for _, child := range children.Keys() {
			spec := children.Val(child)
			resolved := m.results[child]
			sm, isMap := spec.(*ojson.OMap)
			isLocal := isMap && !ojson.Truthy(sm.Val("github")) && !ojson.Truthy(sm.Val("gitlab"))
			switch {
			case !isLocal || resolved == nil || resolved.Val("source") != "local":
				out.Set(child, spec)
			case ojson.Truthy(resolved.Val("dependencies")) && !ojson.Truthy(resolved.Val("error")):
				merged := sm.Copy()
				merged.Set("dependencies", resolved.Val("dependencies"))
				out.Set(child, merged)
			default:
				out.Set(child, m.localLockItem(resolved, true))
			}
		}
		item.Set("dependencies", out)
	}
	item.Set("loaded", loadedField(e))
	item.Set("found", found(e))
	if ojson.Truthy(e.Val("errors")) && !nested {
		item.Set("errors", e.Val("errors"))
	}
	return item
}
