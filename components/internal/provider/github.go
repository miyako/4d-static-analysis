package provider

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/miyako/4d-static-analysis/components/internal/ojson"
	"github.com/miyako/4d-static-analysis/components/internal/version"
)

// GitHub is a minimal REST client for releases. The token goes to the API host only; asset downloads follow
// redirects without it.
type GitHub struct {
	H   *HTTP
	API string
}

func NewGitHub(token string, offline bool) *GitHub {
	g := &GitHub{API: DefaultAPI}
	g.H = &HTTP{Token: token, OfflineM: offline, NameStr: "GitHub", TokenHost: "api.github.com",
		TokenHeader: func(t string) (string, string) { return "Authorization", "Bearer " + t },
		Extra:       func(h http.Header) { h.Set("X-GitHub-Api-Version", "2022-11-28") }}
	return g
}

// SetAPI points the client at another API base (a loopback mock in tests, or GitHub Enterprise). The token is
// then sent to that host only.
func (g *GitHub) SetAPI(base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("invalid GitHub API URL %q", base)
	}
	g.API = base
	g.H.TokenHost = u.Host
	return nil
}

func (g *GitHub) Name() string   { return "GitHub" }
func (g *GitHub) HasToken() bool { return g.H.Token != "" }
func (g *GitHub) Offline() bool  { return g.H.OfflineM }

func (g *GitHub) hint() string {
	if g.H.Token != "" {
		return ""
	}
	return " (a token may be required for private repositories)"
}

func (g *GitHub) json(path string) (any, error) {
	data, err := g.H.Request(g.API+path, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	return parseJSON(data)
}

func notFound(msg string) error { return &NotFound{Failure{Msg: msg}} }

func (g *GitHub) Release(repo string, d version.Decl) (*Release, error) {
	switch d.Kind {
	case version.KindLatest, version.KindTag:
		path := "/repos/" + repo + "/releases/latest"
		what := "latest release"
		if d.Kind == version.KindTag {
			path = "/repos/" + repo + "/releases/tags/" + Quote(d.Tag)
			what = fmt.Sprintf("release %s", pyRepr(d.Tag))
		}
		v, err := g.json(path)
		if errors.Is(err, ErrNotFound) {
			return nil, notFound(fmt.Sprintf("%s not found in %s%s", what, repo, g.hint()))
		}
		if err != nil {
			return nil, err
		}
		return parseRelease(v)
	}
	releases, err := g.Releases(repo)
	if err != nil {
		return nil, err
	}
	return selectRelease(repo, releases, d)
}

// selectRelease applies anchor and range selection to a release list.
func selectRelease(repo string, releases []*Release, d version.Decl) (*Release, error) {
	tags := make([]string, len(releases))
	for i, r := range releases {
		tags[i] = r.Tag
	}
	chosen := ""
	if d.Kind == version.KindAnchor {
		c, err := version.AnchorPick(tags, d.Anchor)
		if err != nil {
			return nil, err
		}
		chosen = c
	} else {
		var matching []string
		for _, t := range tags {
			if version.Satisfies(t, d.Ranges) {
				matching = append(matching, t)
			}
		}
		if len(matching) > 0 {
			chosen = version.MaxSemver(matching)
		}
	}
	if chosen == "" {
		what := "the range"
		if d.Kind == version.KindAnchor {
			what = "4D:" + d.Anchor
		}
		return nil, &version.Failure{Msg: fmt.Sprintf("no release of %s matches %s", repo, what)}
	}
	for _, r := range releases {
		if r.Tag == chosen {
			return r, nil
		}
	}
	return nil, &version.Failure{Msg: "release vanished"}
}

// Releases lists published (non-draft, non-prerelease) releases, at most 10 pages of 100.
func (g *GitHub) Releases(repo string) ([]*Release, error) {
	var out []*Release
	for page := 1; page <= 10; page++ {
		v, err := g.json(fmt.Sprintf("/repos/%s/releases?per_page=100&page=%d", repo, page))
		if errors.Is(err, ErrNotFound) {
			return nil, notFound(fmt.Sprintf("repository %s not found%s", repo, g.hint()))
		}
		if err != nil {
			return nil, err
		}
		list, ok := v.([]any)
		if !ok {
			return nil, &version.Failure{Msg: "unexpected releases payload"}
		}
		for _, item := range list {
			o, ok := item.(*ojson.OMap)
			if !ok || ojson.Truthy(o.Val("draft")) || ojson.Truthy(o.Val("prerelease")) {
				continue
			}
			r, err := parseRelease(o)
			if err != nil {
				return nil, err
			}
			out = append(out, r)
		}
		if len(list) < 100 {
			break
		}
	}
	return out, nil
}

// ChildDependencies reads Project/Sources/dependencies.json from the repository at the tag: 4D does not read it
// from the release archive.
func (g *GitHub) ChildDependencies(repo, tag string) (*ojson.OMap, error) {
	path := "/repos/" + repo + "/contents/Project/Sources/dependencies.json?ref=" + Quote(tag)
	data, err := g.H.Request(g.API+path, "application/vnd.github.raw+json")
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	where := fmt.Sprintf("Project/Sources/dependencies.json of %s@%s", repo, tag)
	value, err := ojson.Parse(ojson.StripBOM(data))
	if err != nil {
		return nil, &version.Failure{Msg: fmt.Sprintf("cannot parse %s: %v", where, err)}
	}
	deps, err := ManifestDependencies(value)
	if err != nil {
		return nil, &version.Failure{Msg: fmt.Sprintf("invalid %s: %v", where, err)}
	}
	if deps.Len() == 0 {
		return nil, nil
	}
	return deps, nil
}

func (g *GitHub) Download(a *Asset) ([]byte, error) {
	if g.H.Token != "" {
		return g.H.Request(a.URL, "application/octet-stream")
	}
	return g.H.Request(a.BrowserDownloadURL, "application/octet-stream")
}

func (g *GitHub) ArchiveURL(a *Asset) string {
	if g.H.Token != "" {
		return a.URL
	}
	return a.BrowserDownloadURL
}

// pyRepr renders a string the way Python's repr() does for the plain tags and names that appear in messages.
func pyRepr(s string) string {
	quote := "'"
	for _, c := range s {
		if c == '\'' {
			quote = `"`
		}
	}
	return quote + s + quote
}
