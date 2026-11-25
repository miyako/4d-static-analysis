package provider

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miyako/4d-static-analysis/components/internal/testutil"
	"github.com/miyako/4d-static-analysis/components/internal/version"
)

func newGH(t *testing.T, s *testutil.Server, token string) *GitHub {
	t.Helper()
	g := NewGitHub(token, false)
	if err := g.SetAPI(s.URL); err != nil {
		t.Fatal(err)
	}
	g.H.Sleep = func(time.Duration) {}
	return g
}

func repoWith(tags ...string) *testutil.Repo {
	r := &testutil.Repo{}
	for _, tag := range tags {
		r.Releases = append(r.Releases, testutil.Rel{Tag: tag, Zip: testutil.ComponentZip("C", true, nil)})
	}
	return r
}

func TestPaginationAndFiltering(t *testing.T) {
	s := testutil.New()
	defer s.Close()
	var tags []string
	for i := 0; i < 150; i++ {
		tags = append(tags, "1.0."+strconv.Itoa(i))
	}
	r := repoWith(tags...)
	r.Releases = append(r.Releases, testutil.Rel{Tag: "9.0.0", Prerelease: true}, testutil.Rel{Tag: "9.1.0", Draft: true})
	s.Repos["o/C"] = r
	g := newGH(t, s, "")
	rels, err := g.Releases("o/C")
	if err != nil || len(rels) != 150 {
		t.Fatalf("%d releases, %v: drafts and prereleases are ignored and every page is read", len(rels), err)
	}
	if got := s.Paths(); len(got) != 2 {
		t.Fatalf("two pages expected: %v", got)
	}
}

func TestSelection(t *testing.T) {
	s := testutil.New()
	defer s.Close()
	s.Repos["o/C"] = repoWith("20.0.1", "20.1.0", "21.0", "21R2", "21R5", "22.0")
	g := newGH(t, s, "")
	cases := []struct {
		d    version.Decl
		want string
	}{
		{version.Decl{Kind: version.KindLatest}, "22.0"},
		{version.Decl{Kind: version.KindTag, Tag: "21.0"}, "21.0"},
		{version.Decl{Kind: version.KindAnchor, Anchor: "21R5"}, "21R5"},
		{version.Decl{Kind: version.KindAnchor, Anchor: "21"}, "21.0"},
	}
	for _, c := range cases {
		r, err := g.Release("o/C", c.d)
		if err != nil || r.Tag != c.want {
			t.Errorf("%+v: %v %v", c.d, r, err)
		}
	}
	rng, _ := version.ParseRange("^20.0.0")
	if r, err := g.Release("o/C", version.Decl{Kind: version.KindRange, Ranges: rng}); err != nil || r.Tag != "20.1.0" {
		t.Errorf("range: %v %v", r, err)
	}
	rng, _ = version.ParseRange("^99.0.0")
	if _, err := g.Release("o/C", version.Decl{Kind: version.KindRange, Ranges: rng}); err == nil || !strings.Contains(err.Error(), "no release of o/C matches the range") {
		t.Errorf("no match: %v", err)
	}
	var nf *NotFound
	if _, err := g.Release("o/missing", version.Decl{Kind: version.KindLatest}); !errors.As(err, &nf) {
		t.Errorf("missing repo must be NotFound: %v", err)
	}
	if _, err := g.Release("o/C", version.Decl{Kind: version.KindTag, Tag: "0.0.0"}); !errors.As(err, &nf) || !strings.Contains(err.Error(), "release '0.0.0' not found in o/C") {
		t.Errorf("missing tag: %v", err)
	}
}

func TestTokenOnlyToAPIHostAndStrippedOnRedirect(t *testing.T) {
	var asset atomic.Value
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asset.Store(r.Header.Get("Authorization"))
		w.Write([]byte("zip"))
	}))
	defer cdn.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			http.Error(w, "no", 401)
			return
		}
		http.Redirect(w, r, cdn.URL+"/blob", http.StatusFound)
	}))
	defer api.Close()
	g := NewGitHub("secret", false)
	if err := g.SetAPI(api.URL); err != nil {
		t.Fatal(err)
	}
	data, err := g.Download(&Asset{URL: api.URL + "/assets/1"})
	if err != nil || string(data) != "zip" {
		t.Fatalf("%q %v", data, err)
	}
	if v := asset.Load(); v != "" {
		t.Fatalf("token leaked to the redirect target: %v", v)
	}
}

func TestRejectedTokenIsNotRetried(t *testing.T) {
	s := testutil.New()
	defer s.Close()
	s.Repos["o/C"] = repoWith("1.0.0")
	s.Token = "right"
	g := newGH(t, s, "wrong")
	var auth *AuthFailure
	_, err := g.Release("o/C", version.Decl{Kind: version.KindLatest})
	if !errors.As(err, &auth) {
		t.Fatalf("want AuthFailure: %v", err)
	}
	var f *version.Failure
	if !errors.As(err, &f) {
		t.Fatal("AuthFailure must unwrap to Failure")
	}
	if n := len(s.Calls()); n != 1 {
		t.Fatalf("a rejected token must not be retried: %d requests", n)
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Fatal("the token must not appear in errors")
	}
}

func TestRetryAndRateLimit(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			http.Error(w, "busy", 503)
			return
		}
		w.Write([]byte(`{"tag_name": "1.0.0", "assets": []}`))
	}))
	defer srv.Close()
	g := NewGitHub("", false)
	g.SetAPI(srv.URL)
	var slept []time.Duration
	g.H.Sleep = func(d time.Duration) { slept = append(slept, d) }
	if _, err := g.Release("o/C", version.Decl{Kind: version.KindLatest}); err != nil || n.Load() != 3 || len(slept) != 2 {
		t.Fatalf("retry: %v n=%d slept=%v", err, n.Load(), slept)
	}

	long := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3600")
		http.Error(w, "slow down", 429)
	}))
	defer long.Close()
	g2 := NewGitHub("", false)
	g2.SetAPI(long.URL)
	g2.H.Sleep = func(time.Duration) { t.Fatal("a delay over 60 s must fail promptly, not sleep") }
	if _, err := g2.Release("o/C", version.Decl{Kind: version.KindLatest}); err == nil || !strings.Contains(err.Error(), "retry after 3600 s") || !strings.Contains(err.Error(), "a token raises the limit") {
		t.Fatalf("long retry: %v", err)
	}
}

func TestOfflineMakesNoRequest(t *testing.T) {
	s := testutil.New()
	defer s.Close()
	g := NewGitHub("", true)
	g.SetAPI(s.URL)
	if _, err := g.Release("o/C", version.Decl{Kind: version.KindLatest}); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("%v", err)
	}
	if len(s.Calls()) != 0 {
		t.Fatal("offline must not touch the network")
	}
}

func TestQuote(t *testing.T) {
	if got := Quote("a b/c+d~e.f-g_h"); got != "a%20b%2Fc%2Bd~e.f-g_h" {
		t.Fatal(got)
	}
}
