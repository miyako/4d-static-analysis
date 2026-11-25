// Package inventory builds the portable dependency inventory: deterministic for identical resolution inputs, with
// no absolute or temporary paths, no environment details and no credentials.
package inventory

import (
	"sort"
	"strings"

	"github.com/miyako/4d-static-analysis/components/internal/ojson"
)

const SchemaVersion = 1

// Scrubber replaces local path prefixes in messages by stable placeholders.
type Scrubber struct{ pairs [][2]string }

func NewScrubber(report *ojson.OMap) *Scrubber {
	s := &Scrubber{}
	for _, kv := range [][2]string{{"package", "<project>"}, {"dependenciesFolder", "<dependencies>"}, {"archiveStore", "<archives>"}, {"home", "<home>"}} {
		if v, ok := report.Val(kv[0]).(string); ok && v != "" {
			s.pairs = append(s.pairs, [2]string{v, kv[1]})
		}
	}
	sort.SliceStable(s.pairs, func(i, j int) bool { return len(s.pairs[i][0]) > len(s.pairs[j][0]) })
	return s
}

func (s *Scrubber) Scrub(text string) string {
	for _, p := range s.pairs {
		text = strings.ReplaceAll(text, p[0], p[1])
	}
	return text
}

func strList(v any, scrub *Scrubber) []any {
	out := []any{}
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			out = append(out, scrub.Scrub(ojson.Str(x)))
		}
	}
	return out
}

func status(e *ojson.OMap) string {
	switch {
	case ojson.Truthy(e.Val("error")):
		return "incomplete"
	case ojson.Truthy(e.Val("overloadedBy")):
		return "provided-by-components"
	case e.Val("source") == "planned":
		return "planned"
	case e.Val("source") == "local":
		return "local"
	}
	return "installed"
}

// Build converts a resolution report to the inventory.
func Build(report *ojson.OMap, toolVersion string) *ojson.OMap {
	scrub := NewScrubber(report)
	deps, _ := report.Val("dependencies").(*ojson.OMap)
	names := deps.Keys()
	sort.Strings(names)
	components := []any{}
	for _, name := range names {
		e, _ := deps.Val(name).(*ojson.OMap)
		c := ojson.New().Set("name", name)
		switch {
		case ojson.Truthy(e.Val("github")):
			c.Set("source", "github").Set("repository", e.Val("github"))
		case ojson.Truthy(e.Val("gitlab")):
			c.Set("source", "gitlab").Set("repository", e.Val("gitlab")).Set("host", e.Val("host"))
		default:
			c.Set("source", "local")
		}
		c.Set("declaration", e.Val("declaration"))
		c.Set("requestedVersion", e.Val("version"))
		c.Set("resolved", e.Val("tag"))
		archive := ojson.New().Set("sha256", e.Val("archiveSha256")).Set("size", e.Val("archiveSize")).Set("url", e.Val("archiveURL"))
		c.Set("archive", archive)
		c.Set("status", status(e)).Set("cache", e.Val("reuse")).Set("overloadedBy", e.Val("overloadedBy"))
		children := []any{}
		if d, ok := e.Val("dependencies").(*ojson.OMap); ok {
			keys := d.Keys()
			sort.Strings(keys)
			for _, k := range keys {
				children = append(children, k)
			}
		}
		c.Set("dependencies", children)
		parents := []any{}
		if l, ok := e.Val("declaredBy").([]any); ok {
			ps := make([]string, len(l))
			for i, p := range l {
				ps[i] = ojson.Str(p)
			}
			sort.Strings(ps)
			for _, p := range ps {
				parents = append(parents, p)
			}
		}
		c.Set("declaredBy", parents)
		errs := []any{}
		if l, ok := e.Val("errors").([]any); ok {
			for _, x := range l {
				if o, ok := x.(*ojson.OMap); ok {
					errs = append(errs, scrub.Scrub(ojson.Str(o.Val("message"))))
				}
			}
		} else if ojson.Truthy(e.Val("error")) {
			errs = append(errs, scrub.Scrub(ojson.Str(e.Val("error"))))
		}
		c.Set("errors", errs)
		components = append(components, c)
	}
	problems := []any{}
	complete := true
	if l, ok := report.Val("incomplete").([]any); ok {
		for _, x := range l {
			complete = false
			o := x.(*ojson.OMap)
			problems = append(problems, ojson.New().Set("name", o.Val("name")).Set("reason", scrub.Scrub(ojson.Str(o.Val("reason")))))
		}
	}
	inv := ojson.New().Set("schemaVersion", ojson.Int(SchemaVersion))
	inv.Set("tool", ojson.New().Set("name", "4d-components").Set("version", toolVersion))
	inv.Set("runtime", ojson.New().Set("name", report.Val("runtime")).Set("version", report.Val("runtimeVersion")))
	inv.Set("complete", complete).Set("components", components)
	inv.Set("errors", problems).Set("warnings", strList(report.Val("warnings"), scrub))
	return inv
}
