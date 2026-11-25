package resolver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/miyako/4d-static-analysis/components/internal/ojson"
	"github.com/miyako/4d-static-analysis/components/internal/provider"
)

var anchorSplit = regexp.MustCompile(`^(\d+)(?:R(\d+))?$`)

// gitlabSupport returns the reason this runtime does not load GitLab components, or "". Observed: 4D 21 R3,
// 21 R4 and 21 R5 load them; 4D 21.1 and 21.2 (the 21.x LTS line) do not. Anything else is unobserved.
func (m *Manager) gitlabSupport() string {
	if m.anchor == "" {
		m.warn("GitLab components: 4D loads them from 21 R3 (not in 21.1 or 21.2); pass the runtime version to check this runtime")
		return ""
	}
	g := anchorSplit.FindStringSubmatch(m.anchor)
	major, _ := strconv.Atoi(g[1])
	if g[2] != "" {
		r, _ := strconv.Atoi(g[2])
		if major > 21 || (major == 21 && r >= 3) {
			return ""
		}
	}
	if g[2] == "" && major == 21 {
		return fmt.Sprintf("4D %s does not load GitLab components (observed in 21.1 and 21.2; 21 R3 and later do)", m.anchor)
	}
	if g[2] == "" && major > 21 {
		m.warn("GitLab components: support in 4D %s has not been observed", m.anchor)
		return ""
	}
	return fmt.Sprintf("GitLab support in 4D %s has not been observed (observed only in 21 R3 to 21 R5)", m.anchor)
}

func (m *Manager) resolveGitLab(e, spec *ojson.OMap, pins map[string]*ojson.OMap) *ojson.OMap {
	name := ojson.Str(e.Val("name"))
	repo := strings.Trim(ojson.Str(spec.Val("gitlab")), "/")
	e.Set("gitlab", repo)
	hostV, hasHost := spec.Get("host")
	if hasHost && hostV != nil {
		e.Set("host", hostV)
	}
	if !gitlabRepoRe.MatchString(repo) || !safeRepo(repo) {
		return m.fail(e, fmt.Sprintf("invalid gitlab project %s", ojson.Repr(repo)))
	}
	host := ""
	if hasHost && hostV != nil {
		s, ok := hostV.(string)
		if !ok || !gitlabHostRe.MatchString(s) {
			return m.fail(e, fmt.Sprintf("invalid gitlab host %s: expected https://<host>", ojson.Repr(hostV)))
		}
		host = s
	}
	if reason := m.gitlabSupport(); reason != "" {
		return m.fail(e, reason)
	}
	base := strings.TrimRight(host, "/")
	if base == "" {
		base = provider.GitLabCom
	}
	client, ok := m.gitlabClients[base]
	if !ok {
		if m.gitlabFactory == nil {
			return m.fail(e, "GitLab components are not supported by this build")
		}
		client = m.gitlabFactory(base)
		m.gitlabClients[base] = client
	}
	src := &Source{Provider: "gitlab", Repo: repo, Client: client, Host: host}
	tag, rel, lockVersion, pinned, err := m.pick(name, src, spec, pins)
	if err == nil {
		if pinned {
			e.Set("pinned", true)
		}
		if lockVersion != "" {
			e.Set("version", lockVersion)
		}
		e.Set("tag", tag)
		err = m.materialise(e, src, tag, rel)
	}
	if err != nil {
		if !ojson.Truthy(e.Val("htmlURL")) {
			// Without the release 4D links to the project's releases page and leaves archiveURL empty.
			e.Set("htmlURL", base+"/"+repo+"/-/releases/").Set("archiveURL", "")
			e.Delete("tag")
		}
		return m.fail(e, err.Error())
	}
	return m.finish(e, name)
}
