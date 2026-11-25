package provider

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/miyako/4d-static-analysis/components/internal/ojson"
	"github.com/miyako/4d-static-analysis/components/internal/version"
)

// GitLab reads releases as 4D 21 R3 to 21 R5 do. A release's assets are links: the component zip lives
// elsewhere and GitLab exposes no digest or size. The token is sent to the GitLab host itself only.
type GitLab struct {
	H       *HTTP
	Host    string
	hostSet bool
}

func NewGitLab(host, token string, offline bool) *GitLab {
	host = strings.TrimRight(host, "/")
	u, _ := url.Parse(host)
	name := ""
	if u != nil {
		name = u.Host
	}
	return &GitLab{Host: host, H: &HTTP{Token: token, OfflineM: offline, NameStr: "GitLab", TokenHost: name,
		TokenHeader: func(t string) (string, string) { return "PRIVATE-TOKEN", t }}}
}

func (g *GitLab) Name() string   { return "GitLab" }
func (g *GitLab) HasToken() bool { return g.H.Token != "" }
func (g *GitLab) Offline() bool  { return g.H.OfflineM }

func (g *GitLab) hint() string {
	if g.H.Token != "" {
		return ""
	}
	return fmt.Sprintf(" (a token may be required for private projects: %s)", GitLabTokenEnv)
}

func (g *GitLab) api(repo, rest string) string {
	return g.Host + "/api/v4/projects/" + Quote(repo) + rest
}

func (g *GitLab) json(u string) (any, error) {
	data, err := g.H.Request(u, "application/json")
	if err != nil {
		return nil, err
	}
	return parseJSON(data)
}

// normalise maps a GitLab release to the neutral record: link assets, a relative tag URL.
func normalise(repo string, v any) (*Release, error) {
	o, ok := v.(*ojson.OMap)
	if !ok {
		return nil, &version.Failure{Msg: "unexpected release payload"}
	}
	tag := str(o.Val("tag_name"))
	if tag == "" {
		return nil, &version.Failure{Msg: "release without tag_name"}
	}
	r := &Release{Tag: tag, HTMLURL: "/" + repo + "/-/tags/" + tag, Upcoming: ojson.Truthy(o.Val("upcoming_release"))}
	if assets, ok := o.Val("assets").(*ojson.OMap); ok {
		if links, ok := assets.Val("links").([]any); ok {
			for _, item := range links {
				lo, ok := item.(*ojson.OMap)
				if !ok {
					continue
				}
				u := str(lo.Val("direct_asset_url"))
				if u == "" {
					u = str(lo.Val("url"))
				}
				r.Assets = append(r.Assets, &Asset{Name: str(lo.Val("name")), ID: lo.Val("id"), URL: u, BrowserDownloadURL: u})
			}
		}
	}
	return r, nil
}

func (g *GitLab) Release(repo string, d version.Decl) (*Release, error) {
	switch d.Kind {
	case version.KindLatest, version.KindTag:
		// GitLab's latest release is the most recently released one, not the highest.
		u := g.api(repo, "/releases/permalink/latest")
		what := "latest release"
		if d.Kind == version.KindTag {
			u = g.api(repo, "/releases/"+Quote(d.Tag))
			what = "release " + pyRepr(d.Tag)
		}
		v, err := g.json(u)
		if errors.Is(err, ErrNotFound) {
			return nil, notFound(fmt.Sprintf("%s not found in GitLab project %s%s", what, repo, g.hint()))
		}
		if err != nil {
			return nil, err
		}
		return normalise(repo, v)
	case version.KindHighest:
		d = version.Decl{Kind: version.KindRange, Lock: d.Lock}
		r, err := version.ParseRange("*")
		if err != nil {
			return nil, err
		}
		d.Ranges = r
	}
	releases, err := g.Releases(repo)
	if err != nil {
		return nil, err
	}
	return selectRelease(repo, releases, d)
}

func (g *GitLab) Releases(repo string) ([]*Release, error) {
	var out []*Release
	for page := 1; page <= 10; page++ {
		v, err := g.json(g.api(repo, fmt.Sprintf("/releases?per_page=100&page=%d", page)))
		if errors.Is(err, ErrNotFound) {
			return nil, notFound(fmt.Sprintf("GitLab project %s not found%s", repo, g.hint()))
		}
		if err != nil {
			return nil, err
		}
		list, ok := v.([]any)
		if !ok {
			return nil, &version.Failure{Msg: "unexpected releases payload"}
		}
		for _, item := range list {
			r, err := normalise(repo, item)
			if err != nil {
				return nil, err
			}
			// Upcoming releases (a future release date) are skipped; how 4D treats them is unobserved.
			if !r.Upcoming {
				out = append(out, r)
			}
		}
		if len(list) < 100 {
			break
		}
		if page == 10 {
			return nil, &version.Failure{Msg: "more than 1000 releases: the release list would be incomplete"}
		}
	}
	return out, nil
}

// ChildDependencies: 4D 21 R5 does not follow a GitLab component's own dependencies (cases M1t, M2t). No
// request is made.
func (g *GitLab) ChildDependencies(repo, tag string) (*ojson.OMap, error) { return nil, nil }

func (g *GitLab) Download(a *Asset) ([]byte, error) {
	return g.H.Request(a.URL, "application/octet-stream")
}

func (g *GitLab) ArchiveURL(a *Asset) string { return a.BrowserDownloadURL }

var _ = http.StatusOK
