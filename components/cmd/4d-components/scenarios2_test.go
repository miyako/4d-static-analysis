package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/miyako/4d-static-analysis/components/internal/testutil"
)

func TestDifferentialManifestsAndLocals(t *testing.T) {
	runScenarios(t, []scenario{
		{name: "unparsable and wrongly shaped child manifests", setup: func(w *world) {
			a := addRepo(w, "o/Bad", "1.0.0")
			a.Children["1.0.0"] = "{not json"
			b := addRepo(w, "o/Shape", "1.0.0")
			b.Children["1.0.0"] = `{"dependencies": []}`
			c := addRepo(w, "o/Empty", "1.0.0")
			c.Children["1.0.0"] = `{}`
			d := addRepo(w, "o/Top", "1.0.0")
			d.Children["1.0.0"] = `[1]`
			w.declare(`{"Bad": {"github": "o/Bad"}, "Shape": {"github": "o/Shape"}, "Empty": {"github": "o/Empty"}, "Top": {"github": "o/Top"}}`)
		}, steps: []step{{wantExit: 3, wantTags: map[string]string{"Empty": "1.0.0"}}}},
		{name: "malformed project manifests are usage errors", setup: func(w *world) { w.write("Project/Sources/dependencies.json", `[]`) },
			steps: []step{{wantExit: 2}}},
		{name: "dependencies of the wrong type", setup: func(w *world) { w.write("Project/Sources/dependencies.json", `{"dependencies": []}`) },
			steps: []step{{wantExit: 2}}},
		{name: "empty manifest file", setup: func(w *world) { w.write("Project/Sources/dependencies.json", ``) }, steps: []step{{wantExit: 2}}},
		{name: "manifest without dependencies, and none at all", setup: func(w *world) { w.write("Project/Sources/dependencies.json", `{"version": 2100}`) },
			steps: []step{{}}},
		{name: "no manifest", setup: func(w *world) {}, steps: []step{{}}},
		{name: "wrongly shaped environment4d", setup: func(w *world) {
			w.declare(`{}`)
			w.writeRoot("environment4d.json", `{"dependencies": 3}`)
		}, steps: []step{{wantExit: 2}}},
		{name: "non-object declarations are incomplete", setup: func(w *world) {
			addRepo(w, "o/A", "1.0.0")
			w.declare(`{"S": "string", "N": null, "L": [1], "A": {"github": "o/A"}}`)
		}, steps: []step{{wantExit: 3, wantTags: map[string]string{"A": "1.0.0"}}}},
		{name: "environment4d string entry is a path", setup: func(w *world) {
			localPackage(w, "Loc", "")
			w.declare(`{"Loc": {}}`)
			w.writeRoot("environment4d.json", `{"dependencies": {"Loc": "Loc"}}`)
		}, steps: []step{{}}},
		{name: "GitHub children of a local component are resolved; relative paths from the package parent", setup: func(w *world) {
			addRepo(w, "o/G", "1.0.0", "1.1.0")
			localPackage(w, "Loc", `{"G": {"github": "o/G", "version": "^1.0.0"}, "Sib": {"path": "Sib"}}`)
			localPackage(w, "Sib", "")
			w.declare(`{"Loc": {"path": "Loc"}}`)
		}, steps: []step{{wantTags: map[string]string{"G": "1.1.0"}}}},
		{name: "environment4d relative paths resolve from its own folder", setup: func(w *world) {
			localPackage(w, "Elsewhere/Loc", "")
			w.declare(`{"Loc": {}}`)
			w.writeRoot("environment4d.json", `{"dependencies": {"Loc": {"path": "Elsewhere/Loc"}}}`)
		}, steps: []step{{}}},
		{name: "missing local component and unreadable local manifest", setup: func(w *world) {
			localPackage(w, "Broken", "")
			w.writeRoot("Broken/Project/Sources/dependencies.json", `{oops`)
			w.declare(`{"Missing": {"path": "Nowhere"}, "Broken": {"path": "Broken"}, "Plain": {}}`)
		}, steps: []step{{wantExit: 3}}},
		{name: "local path declared by a GitHub component is not resolved", setup: func(w *world) {
			a := addRepo(w, "o/A", "1.0.0")
			child(a, "1.0.0", `{"Loc": {"path": "Loc"}}`)
			localPackage(w, "Loc", "")
			w.declare(`{"A": {"github": "o/A"}}`)
		}, steps: []step{{}}},
		{name: "children of an overloaded dependency are still resolved", setup: func(w *world) {
			a := addRepo(w, "o/A", "1.0.0")
			child(a, "1.0.0", `{"B": {"github": "o/B"}}`)
			addRepo(w, "o/B", "1.0.0")
			w.declare(`{"A": {"github": "o/A"}}`)
			w.write("Components/A.4dbase/Project/A.4DProject", "{}")
		}, steps: []step{{wantTags: map[string]string{"A": "1.0.0", "B": "1.0.0"}}}},
		{name: "dependency cycles load each component once", setup: func(w *world) {
			x := addRepo(w, "o/X", "1.0.0")
			y := addRepo(w, "o/Y", "1.0.0")
			z := addRepo(w, "o/Z", "1.0.0")
			child(x, "1.0.0", `{"Y": {"github": "o/Y"}, "X": {"github": "o/X"}}`)
			child(y, "1.0.0", `{"Z": {"github": "o/Z"}}`)
			child(z, "1.0.0", `{"X": {"github": "o/X"}}`)
			w.declare(`{"X": {"github": "o/X"}}`)
		}, steps: []step{{wantTags: map[string]string{"X": "1.0.0", "Y": "1.0.0", "Z": "1.0.0"}}}},
		{name: "invalid repositories", setup: func(w *world) {
			w.declare(`{"Bad": {"github": "no-slash"}, "Space": {"github": "o/a b"}, "Empty": {"github": ""}}`)
		}, steps: []step{{wantExit: 3}}},
	})
}

// secondary declarations: the cases M1-M5, C6a-C6c, T, L, F, V and G of the reference's documentation.
func TestDifferentialCombinedDeclarations(t *testing.T) {
	setup := func(parents map[string]string, tags ...string) func(w *world) {
		return func(w *world) {
			addRepo(w, "o/Dep", tags...)
			decl := "{"
			i := 0
			for name, d := range parents {
				r := addRepo(w, "o/"+name, "1.0.0")
				child(r, "1.0.0", `{"Dep": {"github": "o/Dep"`+d+`}}`)
				if i > 0 {
					decl += ", "
				}
				decl += `"` + name + `": {"github": "o/` + name + `"}`
				i++
			}
			w.declare(decl + "}")
		}
	}
	// the map order is random, so each case uses at most one parent order by naming A < B and relying on the
	// declaration order written above; cases that care about the order use two named parents explicitly.
	ordered := func(a, b string, tags ...string) func(w *world) {
		return func(w *world) {
			addRepo(w, "o/Dep", tags...)
			for name, d := range map[string]string{"A": a, "B": b} {
				r := addRepo(w, "o/"+name, "1.0.0")
				child(r, "1.0.0", `{"Dep": {"github": "o/Dep"`+d+`}}`)
			}
			w.declare(`{"A": {"github": "o/A"}, "B": {"github": "o/B"}}`)
		}
	}
	_ = setup
	runScenarios(t, []scenario{
		{name: "overlapping ranges resolve to their intersection", setup: ordered(`, "version": "^1.0.0"`, `, "version": "~1.1.0"`, "1.0.0", "1.1.0", "1.1.5", "1.2.0"),
			steps: []step{{wantTags: map[string]string{"Dep": "1.1.5"}}}},
		{name: "ranges with no common release conflict", setup: ordered(`, "version": "^1.0.0"`, `, "version": "^2.0.0"`, "1.0.0", "1.5.0", "2.0.0"),
			steps: []step{{wantExit: 3, wantTags: map[string]string{"Dep": "1.5.0"}}}},
		{name: "tag against a satisfying range keeps the tag state", setup: ordered(`, "tag": "1.0.0"`, `, "version": "^1.0.0"`, "1.0.0", "1.5.0"),
			steps: []step{{wantTags: map[string]string{"Dep": "1.0.0"}}}},
		{name: "tag that the other does not allow is a conflict", setup: ordered(`, "tag": "1.0.0"`, `, "tag": "2.0.0"`, "1.0.0", "2.0.0"),
			steps: []step{{wantExit: 3, wantTags: map[string]string{"Dep": "1.0.0"}}}},
		{name: "latest conflicts with nothing", setup: ordered(`, "version": "^1.0.0"`, ``, "1.0.0", "1.5.0", "2.0.0"),
			steps: []step{{wantTags: map[string]string{"Dep": "1.5.0"}}}},
		{name: "4d anchor combined with a range", setup: ordered(`, "version": "4d"`, `, "version": "^20.0.0"`, "20.0.1", "20.1.0", "21.0", "21.5"),
			steps: []step{{args: []string{"--runtime-version", "21.2"}, wantTags: map[string]string{"Dep": "20.1.0"}}}},
		{name: "identical secondary declarations are not combined", setup: ordered(`, "version": "^1.0.0"`, `, "version": "^1.0.0"`, "1.0.0", "1.5.0"),
			steps: []step{{wantTags: map[string]string{"Dep": "1.5.0"}}}},
		{name: "three declarations settled one at a time", setup: func(w *world) {
			addRepo(w, "o/Dep", "1.0.0", "1.1.0", "1.2.0", "2.0.0")
			for _, p := range []struct{ n, d string }{{"A", `, "version": ">=1.0.0"`}, {"B", `, "version": "<2.0.0"`}, {"C", `, "version": "~1.1.0"`}} {
				r := addRepo(w, "o/"+p.n, "1.0.0")
				child(r, "1.0.0", `{"Dep": {"github": "o/Dep"`+p.d+`}}`)
			}
			w.declare(`{"A": {"github": "o/A"}, "B": {"github": "o/B"}, "C": {"github": "o/C"}}`)
		}, steps: []step{{wantExit: 3, wantTags: map[string]string{"Dep": "1.2.0"}}}},
		{name: "a conflict restarts with the combined tag's children", setup: func(w *world) {
			dep := addRepo(w, "o/Dep", "1.0.0", "1.1.0")
			child(dep, "1.0.0", `{"Old": {"github": "o/Old"}}`)
			child(dep, "1.1.0", `{"New": {"github": "o/New"}}`)
			addRepo(w, "o/Old", "1.0.0")
			addRepo(w, "o/New", "1.0.0")
			for _, p := range []struct{ n, d string }{{"A", `, "version": "<=1.0.0"`}, {"B", `, "version": ">=1.0.0 <1.1.0"`}} {
				r := addRepo(w, "o/"+p.n, "1.0.0")
				child(r, "1.0.0", `{"Dep": {"github": "o/Dep"`+p.d+`}}`)
			}
			w.declare(`{"A": {"github": "o/A"}, "B": {"github": "o/B"}}`)
		}, steps: []step{{wantTags: map[string]string{"Dep": "1.0.0"}}}},
		{name: "a combined pin is reused", setup: ordered(`, "version": "^1.0.0"`, `, "version": "~1.1.0"`, "1.1.0", "1.1.5"),
			steps: []step{{wantTags: map[string]string{"Dep": "1.1.5"}}, {before: func(w *world, s *side) {
				w.mock.Repos["o/Dep"].Releases = append(w.mock.Repos["o/Dep"].Releases, testutil.Rel{Tag: "1.1.9", Zip: zipOf("Dep")})
			}, wantTags: map[string]string{"Dep": "1.1.5"}}}},
		{name: "secondary declarations naming different repositories", setup: func(w *world) {
			addRepo(w, "o/Dep", "1.0.0")
			addRepo(w, "o/Other", "1.0.0")
			for _, p := range []struct{ n, d string }{{"A", `"o/Dep", "version": "1.0.0"`}, {"B", `"o/Other", "version": "1.0.0"`}} {
				r := addRepo(w, "o/"+p.n, "1.0.0")
				child(r, "1.0.0", `{"Dep": {"github": `+p.d+`}}`)
			}
			w.declare(`{"A": {"github": "o/A"}, "B": {"github": "o/B"}}`)
		}, steps: []step{{wantExit: 3}}},
	})
}

func TestDifferentialAuthOutageAndRateLimit(t *testing.T) {
	runScenarios(t, []scenario{
		{name: "a rejected token is not worked around, even with a stored archive", setup: func(w *world) {
			r := addRepo(w, "o/Priv", "1.0.0")
			r.Private = true
			w.mock.Token = "good"
			w.declare(`{"Priv": {"github": "o/Priv", "tag": "1.0.0"}}`)
		}, steps: []step{
			{env: map[string]string{"DEPENDENCIES_GITHUB_TOKEN": "good"}, wantTags: map[string]string{"Priv": "1.0.0"}},
			{env: map[string]string{"DEPENDENCIES_GITHUB_TOKEN": "bad"}, wantExit: 3},
			// Anonymous access to the private repository is a plain "not found": the stored archive is used, with a warning.
			{wantExit: 0},
		}},
		{name: "a deleted release falls back to the stored archive with a warning", setup: func(w *world) {
			addRepo(w, "o/A", "1.0.0")
			w.declare(`{"A": {"github": "o/A", "tag": "1.0.0"}}`)
		}, steps: []step{
			{wantTags: map[string]string{"A": "1.0.0"}},
			{wantTags: map[string]string{"A": "1.0.0"}, before: func(w *world, s *side) { delete(w.mock.Repos, "o/A") }},
		}},
		{name: "an outage falls back to the stored archive with a warning", setup: func(w *world) {
			addRepo(w, "o/A", "1.0.0")
			w.declare(`{"A": {"github": "o/A", "tag": "1.0.0"}}`)
		}, steps: []step{
			{wantTags: map[string]string{"A": "1.0.0"}},
			{wantTags: map[string]string{"A": "1.0.0"}, before: func(w *world, s *side) { w.mock.Status["/repos/o/A"] = 503 }},
		}},
		{name: "a long retry delay fails at once", setup: func(w *world) {
			addRepo(w, "o/A", "1.0.0")
			w.mock.Status["/repos/o/A"] = 429
			w.mock.Headers["/repos/o/A"] = map[string]string{"Retry-After": "3600"}
			w.declare(`{"A": {"github": "o/A"}}`)
		}, steps: []step{{wantExit: 3}}},
		{name: "rate limit with reset in the far future fails at once", setup: func(w *world) {
			addRepo(w, "o/A", "1.0.0")
			w.mock.Status["/repos/o/A"] = 403
			w.mock.Headers["/repos/o/A"] = map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "99999999999"}
			w.declare(`{"A": {"github": "o/A"}}`)
		}, steps: []step{{wantExit: 3}}},
		{name: "rejected token without a stored archive is incomplete", setup: func(w *world) {
			r := addRepo(w, "o/Priv", "1.0.0")
			r.Private = true
			w.mock.Token = "good"
			w.declare(`{"Priv": {"github": "o/Priv"}}`)
		}, steps: []step{{env: map[string]string{"DEPENDENCIES_GITHUB_TOKEN": "bad"}, wantExit: 3}}},
		{name: "unsafe archives are incomplete", setup: func(w *world) {
			r := addRepo(w, "o/Trav", "1.0.0")
			r.Releases[0].Zip = testutil.ComponentZip("Trav", true, map[string]string{"../escape": "x"})
			r2 := addRepo(w, "o/Abs", "1.0.0")
			r2.Releases[0].Zip = testutil.ComponentZip("Abs", true, map[string]string{"/abs": "x"})
			r3 := addRepo(w, "o/NotPkg", "1.0.0")
			r3.Releases[0].Zip = testutil.ComponentZip("NotPkg", true, nil)[:0]
			r3.Releases[0].Zip = zipOf("Other")
			r3.Releases[0].Zip = testutil.ComponentZip("x", false, nil)
			r3.Releases[0].AssetName = "NotPkg.zip"
			w.declare(`{"Trav": {"github": "o/Trav"}, "Abs": {"github": "o/Abs"}}`)
		}, steps: []step{{wantExit: 3}}},
	})
}

var _ = os.Stat
var _ = filepath.Join
