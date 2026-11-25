package resolver

import (
	"net/url"
	"path/filepath"
	"strings"

	"github.com/miyako/4d-static-analysis/components/internal/ojson"
	"github.com/miyako/4d-static-analysis/components/internal/provider"
)

// Source is where a component comes from: a GitHub repository or a GitLab project on a host.
type Source struct {
	Provider string // github | gitlab
	Repo     string
	Client   provider.Client
	Host     string // declared GitLab host, "" when none
}

func (s *Source) gitlab() bool { return s.Provider == "gitlab" }

func (s *Source) hostName() string {
	h := s.Host
	if h == "" {
		h = provider.GitLabCom
	}
	if u, err := url.Parse(h); err == nil && u.Hostname() != "" {
		// host and port: two instances on one host must not share a cache folder
		return strings.ToLower(strings.ReplaceAll(u.Host, ":", "_"))
	}
	return "gitlab.com"
}

// cacheFolder is where 4D keeps a component's tags: .github/<owner>/<repo>, or .gitlab/[<host>/]<group>/<project>.
func (s *Source) cacheFolder(dependencies string) string {
	if !s.gitlab() {
		return filepath.Join(dependencies, ".github", filepath.FromSlash(s.Repo))
	}
	base := filepath.Join(dependencies, ".gitlab")
	if s.Host != "" {
		base = filepath.Join(base, s.hostName())
	}
	return filepath.Join(base, filepath.FromSlash(s.Repo))
}

func (s *Source) storeFolder(store string) string {
	if s.gitlab() {
		return filepath.Join(store, "gitlab", s.hostName(), filepath.FromSlash(s.Repo))
	}
	return filepath.Join(store, "github", filepath.FromSlash(s.Repo))
}

// matches reports whether a lock pin belongs to this source.
func (s *Source) matches(pin *ojson.OMap) bool {
	if s.gitlab() {
		host := ""
		if ojson.Truthy(pin.Val("host")) {
			host = ojson.Str(pin.Val("host"))
		}
		return ojson.Equal(pin.Val("gitlab"), s.Repo) && host == s.Host
	}
	return ojson.Equal(pin.Val("github"), s.Repo)
}

// safeSegment reports whether a name, tag or repository part can be used as one folder name without escaping
// the folder it is joined to. (A hardening over the reference implementation, which joins them unchecked.)
func safeSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\\x00") && !strings.Contains(s, ":")
}

func safeRepo(repo string) bool {
	for _, part := range strings.Split(repo, "/") {
		if !safeSegment(part) {
			return false
		}
	}
	return true
}
