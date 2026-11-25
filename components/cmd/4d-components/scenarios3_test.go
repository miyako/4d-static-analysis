package main

import (
	"testing"

	"github.com/miyako/4d-static-analysis/components/internal/testutil"
)

func addGitLab(w *world, project string, rels ...testutil.LinkRel) {
	list := rels
	w.mock.GitLab[project] = &list
}

func link(tag string, name string) testutil.LinkRel {
	return testutil.LinkRel{Tag: tag, Links: []testutil.Link{{Name: name, Zip: zipOf("wavplay")}}}
}

func TestDifferentialGitLab(t *testing.T) {
	setup := func(w *world) {
		addGitLab(w, "grp/wavplay", link("1.0.0", "a.zip"), link("1.1.0", "b.zip"), link("2.0.0", "c.zip"), link("1.5.0", "d.zip"))
	}
	runScenarios(t, []scenario{
		{name: "versions resolve as in 4D 21 R5", setup: func(w *world) {
			setup(w)
			w.declare(`{"Highest": {"gitlab": "grp/wavplay"}, "Latest": {"gitlab": "grp/wavplay", "version": "latest"},
				"Range": {"gitlab": "grp/wavplay", "version": "^1.0.0"}, "Exact": {"gitlab": "grp/wavplay", "tag": "1.1.0"}}`)
		}, steps: []step{{args: []string{"--runtime-version", "21R5"}, wantTags: map[string]string{"Highest": "2.0.0", "Latest": "1.5.0", "Range": "1.5.0", "Exact": "1.1.0"}}}},
		{name: "declared host is named in the cache path", setup: func(w *world) {
			setup(w)
			w.declare(`{"H": {"gitlab": "grp/wavplay", "host": "https://gitlab.example.com", "version": "1.0.0"}}`)
		}, steps: []step{{args: []string{"--runtime-version", "21R5"}, wantTags: map[string]string{"H": "1.0.0"}}}},
		{name: "a 21.x LTS runtime and unobserved runtimes", setup: func(w *world) {
			setup(w)
			w.declare(`{"G": {"gitlab": "grp/wavplay"}}`)
		}, steps: []step{
			{args: []string{"--runtime-version", "21.2"}, wantExit: 3},
			{args: []string{"--runtime-version", "20R5"}, wantExit: 3},
			{args: []string{"--runtime-version", "22"}, wantTags: map[string]string{"G": "2.0.0"}},
			{args: []string{}, wantTags: map[string]string{"G": "2.0.0"}},
		}},
		{name: "a missing project and release", setup: func(w *world) {
			setup(w)
			w.declare(`{"Gone": {"gitlab": "grp/gone"}, "NoRel": {"gitlab": "grp/wavplay", "tag": "9.9.9"}, "BadHost": {"gitlab": "grp/wavplay", "host": "http://insecure.example"}, "BadProject": {"gitlab": "single"}}`)
		}, steps: []step{{args: []string{"--runtime-version", "21R5"}, wantExit: 3}}},
		{name: "a release without a link", setup: func(w *world) {
			addGitLab(w, "grp/wavplay", testutil.LinkRel{Tag: "1.0.0"})
			w.declare(`{"G": {"gitlab": "grp/wavplay"}}`)
		}, steps: []step{{args: []string{"--runtime-version", "21R5"}, wantExit: 3}}},
		{name: "with several links the first listed is used; upcoming releases are skipped", setup: func(w *world) {
			addGitLab(w, "grp/wavplay", testutil.LinkRel{Tag: "1.0.0", Links: []testutil.Link{{Name: "first.zip", Zip: zipOf("wavplay")}, {Name: "wavplay.zip", Zip: zipOf("other")}}},
				testutil.LinkRel{Tag: "3.0.0", Upcoming: true, Links: []testutil.Link{{Name: "x.zip", Zip: zipOf("wavplay")}}})
			w.declare(`{"G": {"gitlab": "grp/wavplay"}}`)
		}, steps: []step{{args: []string{"--runtime-version", "21R5"}, wantTags: map[string]string{"G": "1.0.0"}}}},
		{name: "a GitLab component's own dependencies are not followed", setup: func(w *world) {
			setup(w)
			w.declare(`{"G": {"gitlab": "grp/wavplay", "tag": "1.0.0"}}`)
		}, steps: []step{{args: []string{"--runtime-version", "21R5"}, wantTags: map[string]string{"G": "1.0.0"}}}},
		{name: "pins and offline runs use the archive store", setup: func(w *world) {
			setup(w)
			w.declare(`{"G": {"gitlab": "grp/wavplay", "version": "^1.0.0"}}`)
		}, steps: []step{
			{args: []string{"--runtime-version", "21R5"}, wantTags: map[string]string{"G": "1.5.0"}},
			{args: []string{"--runtime-version", "21R5", "--offline"}, wantTags: map[string]string{"G": "1.5.0"}},
		}},
		{name: "GitHub and GitLab side by side with a 4d anchor", setup: func(w *world) {
			setup(w)
			addRepo(w, "o/Follow", "21R5", "21R5.1")
			w.declare(`{"G": {"gitlab": "grp/wavplay", "version": "4d"}, "F": {"github": "o/Follow", "version": "4d"}}`)
		}, steps: []step{{args: []string{"--runtime-version", "21R5"}, wantTags: map[string]string{"F": "21R5.1"}}}},
	})
}
