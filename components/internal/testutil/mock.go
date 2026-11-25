// Package testutil provides an in-memory GitHub/GitLab mock server and fixture builders. Unit tests never
// reach a live provider.
package testutil

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Rel is one release served by the mock.
type Rel struct {
	Tag        string
	Zip        []byte
	Prerelease bool
	Draft      bool
	AssetName  string // default <repo>.zip
	NoAsset    bool
	NoDigest   bool
	BadDigest  bool
}

// Repo is a mock repository.
type Repo struct {
	Releases []Rel
	Latest   string            // tag served by /releases/latest (default: last release)
	Children map[string]string // tag -> Project/Sources/dependencies.json contents
	Private  bool              // requires Token
}

// LinkRel is a GitLab release with link assets.
type LinkRel struct {
	Tag      string
	Links    []Link
	Upcoming bool
}

type Link struct {
	Name string
	Zip  []byte
}

// Request is a recorded request.
type Request struct {
	Method, Path, Query, Auth, PrivateToken, Host string
}

// Server is the mock.
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	Repos    map[string]*Repo
	GitLab   map[string]*[]LinkRel
	Token    string                       // the credential private repositories accept
	Status   map[string]int               // path prefix -> forced status
	Headers  map[string]map[string]string // path prefix -> response headers for the forced status
	Requests []Request
	PageSize int
}

func New() *Server {
	s := &Server{Repos: map[string]*Repo{}, GitLab: map[string]*[]LinkRel{}, Status: map[string]int{}, Headers: map[string]map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

func (s *Server) Calls() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.Requests...)
}

func (s *Server) ResetCalls() { s.mu.Lock(); s.Requests = nil; s.mu.Unlock() }

// Paths returns the request paths (with queries) seen so far.
func (s *Server) Paths() []string {
	var out []string
	for _, r := range s.Calls() {
		p := r.Path
		if r.Query != "" {
			p += "?" + r.Query
		}
		out = append(out, p)
	}
	return out
}

func SHA(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

// ComponentZip builds an interpreted component archive. wrapped adds the <name>.4dbase folder.
func ComponentZip(name string, wrapped bool, extra map[string]string) []byte {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	prefix := ""
	if wrapped {
		prefix = name + ".4dbase/"
	}
	write := func(n, c string) {
		w, _ := z.Create(n)
		w.Write([]byte(c))
	}
	write(prefix+"Project/"+name+".4DProject", "{}")
	write(prefix+"Project/Sources/Methods/m.4dm", "//")
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		write(k, extra[k])
	}
	z.Close()
	return b.Bytes()
}

func (s *Server) record(r *http.Request) {
	s.mu.Lock()
	s.Requests = append(s.Requests, Request{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Private-Token"), r.Host})
	s.mu.Unlock()
}

func (s *Server) releaseJSON(repo string, rel Rel) map[string]any {
	name := rel.AssetName
	if name == "" {
		name = repo[strings.Index(repo, "/")+1:] + ".zip"
	}
	out := map[string]any{"tag_name": rel.Tag, "draft": rel.Draft, "prerelease": rel.Prerelease,
		"html_url": fmt.Sprintf("https://github.com/%s/releases/tag/%s", repo, rel.Tag), "assets": []any{}}
	if rel.NoAsset {
		return out
	}
	asset := map[string]any{"name": name, "id": 1, "size": len(rel.Zip),
		"url":                  fmt.Sprintf("%s/assets/%s/%s", s.URL, repo, url.PathEscape(rel.Tag)),
		"browser_download_url": fmt.Sprintf("%s/download/%s/%s/%s", s.URL, repo, url.PathEscape(rel.Tag), name)}
	if !rel.NoDigest {
		d := SHA(rel.Zip)
		if rel.BadDigest {
			d = strings.Repeat("0", 64)
		}
		asset["digest"] = "sha256:" + d
	}
	out["assets"] = []any{asset}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.record(r)
	s.mu.Lock()
	for prefix, code := range s.Status {
		if strings.HasPrefix(r.URL.Path, prefix) {
			for k, v := range s.Headers[prefix] {
				w.Header().Set(k, v)
			}
			s.mu.Unlock()
			http.Error(w, "forced", code)
			return
		}
	}
	s.mu.Unlock()
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/v4/projects/") {
		s.serveGitLab(w, r)
		return
	}
	if strings.HasPrefix(path, "/dl/") {
		s.serveGitLabDownload(w, r)
		return
	}
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	switch {
	case len(segs) >= 4 && segs[0] == "repos":
		repoName := segs[1] + "/" + segs[2]
		repo, ok := s.Repos[repoName]
		auth := r.Header.Get("Authorization")
		if !ok {
			http.NotFound(w, r)
			return
		}
		if auth != "" && auth != "Bearer "+s.Token {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		if repo.Private && auth == "" {
			http.NotFound(w, r)
			return
		}
		rest := strings.Join(segs[3:], "/")
		switch {
		case rest == "releases/latest":
			latest := repo.Latest
			if latest == "" && len(repo.Releases) > 0 {
				latest = repo.Releases[len(repo.Releases)-1].Tag
			}
			for _, rel := range repo.Releases {
				if rel.Tag == latest {
					writeJSON(w, s.releaseJSON(repoName, rel))
					return
				}
			}
		case strings.HasPrefix(rest, "releases/tags/"):
			tag, _ := url.PathUnescape(strings.TrimPrefix(rest, "releases/tags/"))
			for _, rel := range repo.Releases {
				if rel.Tag == tag {
					writeJSON(w, s.releaseJSON(repoName, rel))
					return
				}
			}
		case rest == "releases":
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			size := s.PageSize
			if size == 0 {
				size = 100
			}
			list := []any{}
			for i, rel := range repo.Releases {
				if i >= (page-1)*size && i < page*size {
					list = append(list, s.releaseJSON(repoName, rel))
				}
			}
			writeJSON(w, list)
			return
		case rest == "contents/Project/Sources/dependencies.json":
			if body, ok := repo.Children[r.URL.Query().Get("ref")]; ok {
				w.Write([]byte(body))
				return
			}
		}
	case len(segs) == 4 && segs[0] == "assets":
		s.serveAsset(w, segs[1]+"/"+segs[2], segs[3], r)
		return
	case len(segs) == 5 && segs[0] == "download":
		s.serveAsset(w, segs[1]+"/"+segs[2], segs[3], r)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) serveAsset(w http.ResponseWriter, repoName, tagEsc string, r *http.Request) {
	tag, _ := url.PathUnescape(tagEsc)
	if repo, ok := s.Repos[repoName]; ok {
		for _, rel := range repo.Releases {
			if rel.Tag == tag {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Write(rel.Zip)
				return
			}
		}
	}
	http.NotFound(w, r)
}

func (s *Server) serveGitLab(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.EscapedPath(), "/api/v4/projects/")
	enc, tail, _ := strings.Cut(rest, "/")
	project, _ := url.PathUnescape(enc)
	rels, ok := s.GitLab[project]
	if !ok {
		http.NotFound(w, r)
		return
	}
	one := func(rel LinkRel) map[string]any {
		links := []any{}
		for i, l := range rel.Links {
			u := fmt.Sprintf("%s/dl/%s/%s/%d", s.URL, enc, url.PathEscape(rel.Tag), i)
			links = append(links, map[string]any{"id": i + 1, "name": l.Name, "url": u, "direct_asset_url": u})
		}
		m := map[string]any{"tag_name": rel.Tag, "assets": map[string]any{"links": links}}
		if rel.Upcoming {
			m["upcoming_release"] = true
		}
		return m
	}
	switch {
	case tail == "releases/permalink/latest":
		for i := len(*rels) - 1; i >= 0; i-- {
			if !(*rels)[i].Upcoming {
				writeJSON(w, one((*rels)[i]))
				return
			}
		}
	case tail == "releases":
		list := []any{}
		if p := r.URL.Query().Get("page"); p == "" || p == "1" {
			for i := len(*rels) - 1; i >= 0; i-- {
				list = append(list, one((*rels)[i]))
			}
		}
		writeJSON(w, list)
		return
	case strings.HasPrefix(tail, "releases/"):
		tag, _ := url.PathUnescape(strings.TrimPrefix(tail, "releases/"))
		for _, rel := range *rels {
			if rel.Tag == tag {
				writeJSON(w, one(rel))
				return
			}
		}
	}
	http.NotFound(w, r)
}

func (s *Server) serveGitLabDownload(w http.ResponseWriter, r *http.Request) {
	segs := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/dl/"), "/")
	if len(segs) != 3 {
		http.NotFound(w, r)
		return
	}
	project, _ := url.PathUnescape(segs[0])
	tag, _ := url.PathUnescape(segs[1])
	idx, _ := strconv.Atoi(segs[2])
	if rels, ok := s.GitLab[project]; ok {
		for _, rel := range *rels {
			if rel.Tag == tag && idx < len(rel.Links) {
				w.Write(rel.Links[idx].Zip)
				return
			}
		}
	}
	http.NotFound(w, r)
}
