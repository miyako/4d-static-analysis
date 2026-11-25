package resolver

import (
	"fmt"
	"strings"

	"github.com/miyako/4d-static-analysis/components/internal/ojson"
	"github.com/miyako/4d-static-analysis/components/internal/provider"
	"github.com/miyako/4d-static-analysis/components/internal/version"
)

// maxBySemver is Python's max(tags, key=lambda t: _semver(t) or ()): a tag with no numeric key sorts lowest and
// the first of equal keys wins.
func maxBySemver(tags []string) string {
	best, bestKey, found, bestOK := "", [3]int{}, false, false
	for _, t := range tags {
		k, ok := version.Semver(t)
		better := !found
		if found {
			switch {
			case ok && !bestOK:
				better = true
			case ok && bestOK:
				better = cmpKey(k, bestKey) > 0
			}
		}
		if better {
			best, bestKey, bestOK, found = t, k, ok, true
		}
	}
	return best
}

func cmpKey(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// combinedRelease settles differing secondary declarations of one GitHub dependency as 4D 21.2 does: one at a
// time, in the order 4D meets them, each against the current state (cases C6a-C6c, L1-L5, F1-F7, T1-T4, V1, V2,
// E1, A1, A2, G1-G3, M1-M5). The state starts as the first declaration.
//
//   - A declaration conflicts with the state when they cannot meet: a `tag` the other does not allow, or
//     ranges/"4d" anchors that no release satisfies together. `latest` never conflicts. The state is kept; the
//     dependency is still installed and loaded, but the components that declare it are not.
//   - When the state or the declaration uses the `tag` key, or the state is already a combined result, the state
//     is kept. An exact `version` is not a `tag`.
//   - Otherwise the state becomes the highest release that satisfies both, `latest` meaning the newest release,
//     recorded as the lock `version`; with none, the state is kept. A combined state is an exact tag: a later
//     conflict with it drops the lock `version`.
//
// It returns the tag, the release (nil when no lookup was needed), the lock version and the conflict message.
func (m *Manager) combinedRelease(name, repo string, specs []any, pins map[string]*ojson.OMap) (string, *provider.Release, string, string, error) {
	described := make([]string, len(specs))
	asMaps := make([]*ojson.OMap, len(specs))
	for i, s := range specs {
		described[i] = ojson.PyDumps(s)
		asMaps[i], _ = s.(*ojson.OMap)
	}
	for _, s := range asMaps {
		if s == nil || strings.Trim(ojson.Str(getOr(s, "github", "")), "/") != repo {
			return "", nil, "", "", failf("secondary declarations of %s name different repositories: %s", name, strings.Join(described, ", "))
		}
	}
	pin := pins[name]
	pinTag := ""
	if pin != nil && ojson.Equal(pin.Val("github"), repo) {
		pinTag = ojson.Str(pin.Val("tag"))
	}
	client := m.github
	var releases []*provider.Release
	loaded := false
	var listErr error
	tags := func() []string {
		if !loaded {
			releases, listErr = client.Releases(repo)
			loaded = true
		}
		out := make([]string, len(releases))
		for i, r := range releases {
			out[i] = r.Tag
		}
		return out
	}
	meet := func(a, b version.Decl) bool {
		if a.Kind == version.KindLatest || b.Kind == version.KindLatest {
			return true
		}
		if a.Kind == version.KindTag {
			return b.Allows(a.Tag)
		}
		if b.Kind == version.KindTag {
			return a.Allows(b.Tag)
		}
		if pinTag != "" && a.Allows(pinTag) && b.Allows(pinTag) {
			return true
		}
		for _, t := range tags() {
			if a.Allows(t) && b.Allows(t) {
				return true
			}
		}
		return false
	}
	state, err := m.classify(asMaps[0], false)
	if err != nil {
		return "", nil, "", "", err
	}
	keep := ojson.Truthy(asMaps[0].Val("tag"))
	var result *struct {
		tag string
		rel *provider.Release
	}
	conflict := ""
	for _, spec := range asMaps[1:] {
		other, err := m.classify(spec, false)
		if err != nil {
			return "", nil, "", "", err
		}
		met := meet(state, other)
		if listErr != nil {
			return "", nil, "", "", listErr
		}
		if !met {
			if conflict == "" {
				against := described[0]
				if result != nil {
					against = result.tag
				}
				conflict = fmt.Sprintf("secondary dependency %s is disabled by a version conflict: %s is incompatible with %s",
					name, ojson.PyDumps(spec), against)
			}
			continue
		}
		if keep || result != nil || ojson.Truthy(spec.Val("tag")) {
			continue
		}
		var constraints []version.Decl
		for _, c := range []version.Decl{state, other} {
			if c.Kind != version.KindLatest {
				constraints = append(constraints, c)
			}
		}
		latest := len(constraints) < 2
		allAllow := func(t string) bool {
			for _, c := range constraints {
				if !c.Allows(t) {
					return false
				}
			}
			return true
		}
		if pinTag != "" && !latest && ojson.Str(pin.Val("version")) == pinTag && allAllow(pinTag) {
			result = &struct {
				tag string
				rel *provider.Release
			}{pinTag, nil}
		} else {
			var pool []string
			for _, t := range tags() {
				if allAllow(t) {
					pool = append(pool, t)
				}
			}
			if listErr != nil {
				return "", nil, "", "", listErr
			}
			if latest {
				newest, err := client.Release(repo, version.Decl{Kind: version.KindLatest})
				if err != nil {
					return "", nil, "", "", err
				}
				var only []string
				for _, t := range pool {
					if t == newest.Tag {
						only = append(only, t)
					}
				}
				pool = only
			}
			if len(pool) == 0 {
				continue
			}
			var chosen string
			anchored := false
			for _, c := range constraints {
				if c.Kind == version.KindAnchor {
					chosen, err = version.AnchorPick(pool, c.Anchor)
					if err != nil {
						return "", nil, "", "", err
					}
					anchored = true
					break
				}
			}
			if !anchored {
				chosen = maxBySemver(pool)
			}
			var rel *provider.Release
			for _, r := range releases {
				if r.Tag == chosen {
					rel = r
					break
				}
			}
			result = &struct {
				tag string
				rel *provider.Release
			}{chosen, rel}
		}
		state = version.Decl{Kind: version.KindTag, Tag: result.tag}
	}
	if result != nil {
		lock := result.tag
		if conflict != "" {
			lock = ""
		}
		return result.tag, result.rel, lock, conflict, nil
	}
	src := &Source{Provider: "github", Repo: repo, Client: client}
	tag, rel, lock, _, err := m.pick(name, src, asMaps[0], pins)
	return tag, rel, lock, conflict, err
}

// getOr is Python's dict.get(key, default): the default applies only to a missing key, not to null.
func getOr(o *ojson.OMap, key string, def any) any {
	if v, ok := o.Get(key); ok {
		return v
	}
	return def
}
