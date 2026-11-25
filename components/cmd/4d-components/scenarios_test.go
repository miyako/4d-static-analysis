package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/miyako/4d-static-analysis/components/internal/testutil"
)

func (w *world) writeRoot(rel, content string) {
	for _, s := range w.sides {
		p := filepath.Join(s.root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
}

func child(r *testutil.Repo, tag, deps string) { r.Children[tag] = `{"dependencies": ` + deps + `}` }

func localPackage(w *world, rel, deps string) {
	name := filepath.Base(rel)
	w.writeRoot(rel+"/Project/"+name+".4DProject", "{}")
	if deps != "" {
		w.writeRoot(rel+"/Project/Sources/dependencies.json", `{"dependencies": `+deps+`}`)
	}
}

func TestDifferentialBasics(t *testing.T) {
	runScenarios(t, []scenario{
		{name: "latest exact tag and range", setup: func(w *world) {
			addRepo(w, "o/Latest", "1.0.0", "1.1.0")
			addRepo(w, "o/Exact", "1.0.0", "1.1.0")
			addRepo(w, "o/Range", "1.0.0", "1.1.0", "2.0.0", "v1.2.0")
			w.declare(`{"Latest": {"github": "o/Latest"}, "Exact": {"github": "o/Exact", "tag": "1.0.0"},
				"Range": {"github": "o/Range", "version": "^1.0.0"}, "Version": {"github": "o/Exact", "version": "1.1.0"}}`)
		}, steps: []step{{wantTags: map[string]string{"Latest": "1.1.0", "Exact": "1.0.0", "Range": "v1.2.0", "Version": "1.1.0"}}}},
		{name: "4d anchor on 21R5 and 21.2", setup: func(w *world) {
			addRepo(w, "o/Follow", "20.0.1", "21.0", "21.5", "21R2", "21R5", "21R5.1", "21R6")
			w.declare(`{"Follow": {"github": "o/Follow", "version": "4d"}}`)
		}, steps: []step{
			{args: []string{"--runtime-version", "21R5"}, wantTags: map[string]string{"Follow": "21R5.1"}},
			{args: []string{"--runtime-version", "21.2", "--update"}, wantTags: map[string]string{"Follow": "21.5"}},
		}},
		{name: "4d without a runtime version is incomplete", setup: func(w *world) {
			addRepo(w, "o/Follow", "21.0")
			w.declare(`{"Follow": {"github": "o/Follow", "version": "4d"}}`)
		}, steps: []step{{wantExit: 3}}},
		{name: "recursion, flat lock, pins and update", setup: func(w *world) {
			a := addRepo(w, "o/A", "1.0.0")
			child(a, "1.0.0", `{"B": {"github": "o/B", "version": "^1.0.0"}}`)
			addRepo(w, "o/B", "1.0.0", "1.1.0")
			w.declare(`{"A": {"github": "o/A"}}`)
		}, steps: []step{
			{wantTags: map[string]string{"A": "1.0.0", "B": "1.1.0"}},
			{before: func(w *world, s *side) {
				w.mock.Repos["o/B"].Releases = append(w.mock.Repos["o/B"].Releases, testutil.Rel{Tag: "1.2.0", Zip: zipOf("B")})
			}, wantTags: map[string]string{"B": "1.1.0"}},
			{args: []string{"--update"}, wantTags: map[string]string{"B": "1.2.0"}},
		}},
		{name: "direct declaration wins over a child's", setup: func(w *world) {
			a := addRepo(w, "o/A", "1.0.0")
			child(a, "1.0.0", `{"B": {"github": "o/B", "tag": "1.0.0"}}`)
			addRepo(w, "o/B", "1.0.0", "2.0.0")
			w.declare(`{"B": {"github": "o/B", "tag": "2.0.0"}, "A": {"github": "o/A"}}`)
		}, steps: []step{{wantTags: map[string]string{"B": "2.0.0"}}}},
		{name: "environment4d overrides, ignores env-only names, Components overload", setup: func(w *world) {
			addRepo(w, "o/A", "1.0.0")
			addRepo(w, "o/B", "1.0.0")
			addRepo(w, "o/Only", "1.0.0")
			w.declare(`{"A": {"github": "o/A"}, "B": {"github": "o/B"}}`)
			w.writeRoot("environment4d.json", `{"dependencies": {"A": {"github": "o/A", "tag": "1.0.0"}, "Only": {"github": "o/Only"}}}`)
			w.write("Components/B.4dbase/Project/B.4DProject", "{}")
		}, steps: []step{{wantTags: map[string]string{"A": "1.0.0", "B": "1.0.0"}}}},
		{name: "offline replays the verified archive and refuses unpinned latest", setup: func(w *world) {
			addRepo(w, "o/A", "1.0.0")
			addRepo(w, "o/B", "1.0.0")
			w.declare(`{"A": {"github": "o/A", "tag": "1.0.0"}}`)
		}, steps: []step{
			{wantTags: map[string]string{"A": "1.0.0"}},
			{args: []string{"--offline"}, wantTags: map[string]string{"A": "1.0.0"}, before: func(w *world, s *side) {
				os.RemoveAll(s.cacheRoot())
			}},
			{args: []string{"--offline"}, wantExit: 3, before: func(w *world, s *side) {
				w.declare(`{"B": {"github": "o/B"}}`)
			}},
		}},
		{name: "tampered archive is discarded and downloaded again", setup: func(w *world) {
			addRepo(w, "o/A", "1.0.0")
			w.declare(`{"A": {"github": "o/A", "tag": "1.0.0"}}`)
		}, steps: []step{
			{wantTags: map[string]string{"A": "1.0.0"}},
			{wantTags: map[string]string{"A": "1.0.0"}, before: func(w *world, s *side) {
				p := filepath.Join(s.store, "github", "o", "A", "1.0.0", "A.zip")
				os.WriteFile(p, []byte("tampered"), 0o644)
			}},
			{args: []string{"--offline"}, wantExit: 3, before: func(w *world, s *side) {
				p := filepath.Join(s.store, "github", "o", "A", "1.0.0", "A.zip")
				os.WriteFile(p, []byte("tampered"), 0o644)
				os.RemoveAll(s.cacheRoot())
			}},
		}},
		{name: "shared cache mode reuses unverified and reports a damaged package", setup: func(w *world) {
			addRepo(w, "o/A", "1.0.0")
			w.declare(`{"A": {"github": "o/A", "tag": "1.0.0"}}`)
		}, steps: []step{
			{noStore: true, wantTags: map[string]string{"A": "1.0.0"}},
			{noStore: true, wantTags: map[string]string{"A": "1.0.0"}},
			{noStore: true, wantExit: 3, before: func(w *world, s *side) {
				os.Remove(filepath.Join(s.cacheRoot(), "tool4d", "Dependencies", ".github", "o", "A", "1.0.0", "Project", "A.4DProject"))
			}},
		}},
		{name: "missing repository and release give an incomplete lock entry", setup: func(w *world) {
			addRepo(w, "o/A", "1.0.0")
			w.declare(`{"Gone": {"github": "o/Gone"}, "NoTag": {"github": "o/A", "tag": "9.9.9"}}`)
		}, steps: []step{{wantExit: 3}}},
		{name: "digest mismatch, missing asset and unknown asset name", setup: func(w *world) {
			r := addRepo(w, "o/Bad", "1.0.0")
			r.Releases[0].BadDigest = true
			r2 := addRepo(w, "o/NoAsset", "1.0.0")
			r2.Releases[0].NoAsset = true
			r3 := addRepo(w, "o/Named", "1.0.0")
			r3.Releases[0].AssetName = "other.zip"
			r4 := addRepo(w, "o/NoDigest", "1.0.0")
			r4.Releases[0].NoDigest = true
			w.declare(`{"Bad": {"github": "o/Bad"}, "NoAsset": {"github": "o/NoAsset"}, "Named": {"github": "o/Named"}, "NoDigest": {"github": "o/NoDigest"}}`)
		}, steps: []step{{wantExit: 3, wantTags: map[string]string{"NoDigest": "1.0.0"}}}},
		{name: "rewrite writes an exact-tag manifest into the workspace copy", setup: func(w *world) {
			a := addRepo(w, "o/A", "1.0.0")
			child(a, "1.0.0", `{"B": {"github": "o/B"}}`)
			addRepo(w, "o/B", "1.0.0", "1.2.0")
			w.declare(`{"A": {"github": "o/A", "extra": 1}}`)
			w.write("Project/Sources/Methods/x.4dm", "//")
		}, steps: []step{{
			before: func(w *world, s *side) {
				ws := filepath.Join(s.root, "W")
				os.RemoveAll(ws)
				os.MkdirAll(filepath.Join(ws, "Project", "Sources"), 0o755)
				os.WriteFile(filepath.Join(ws, "Project", "W.4DProject"), []byte("{}"), 0o644)
				data, _ := os.ReadFile(filepath.Join(s.project, "Project", "Sources", "dependencies.json"))
				os.WriteFile(filepath.Join(ws, "Project", "Sources", "dependencies.json"), data, 0o644)
			},
			rootArgs: func(s *side) []string { return []string{"--rewrite", filepath.Join(s.root, "W")} },
			wantTags: map[string]string{"A": "1.0.0", "B": "1.2.0"},
		}}},
	})
}
