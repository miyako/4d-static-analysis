// Package provider talks to GitHub and GitLab release APIs. Credentials go only to the provider's own API
// host and are never logged; a rejected credential is a hard failure with no anonymous retry (as in 4D).
package provider

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/miyako/4d-static-analysis/components/internal/ojson"
	"github.com/miyako/4d-static-analysis/components/internal/version"
)

const (
	UserAgent      = "miyako-4d-dependencies"
	DefaultAPI     = "https://api.github.com"
	GitLabCom      = "https://gitlab.com"
	MaxResponse    = 1 << 30
	maxRedirects   = 6
	maxRetryDelay  = 60
	GitHubTokenEnv = "DEPENDENCIES_GITHUB_TOKEN"
	GitLabTokenEnv = "DEPENDENCIES_GITLAB_TOKEN"
)

// Failure is a dependency-level failure (it is reported in `incomplete`).
type Failure = version.Failure

// AuthFailure: the provider rejected the request (401, or 403 other than rate limiting). Never worked around.
type AuthFailure struct{ Failure }

// NotFound: the release or repository does not exist, or is invisible without a token.
type NotFound struct{ Failure }

// ErrNotFound is the internal 404 signal (Python's LookupError).
var ErrNotFound = errors.New("not found")

func failf(format string, args ...any) error {
	return &version.Failure{Msg: fmt.Sprintf(format, args...)}
}

// Asset is a downloadable release asset. Digest and Size are absent for GitLab links.
type Asset struct {
	Name               string
	ID                 any
	Size               *int64
	Digest             string
	URL                string
	BrowserDownloadURL string
}

// Release is a provider-neutral release record.
type Release struct {
	Tag      string
	HTMLURL  string
	Assets   []*Asset
	Upcoming bool
}

// Client is the resolver's view of a provider.
type Client interface {
	Name() string
	HasToken() bool
	Offline() bool
	// Release selects one release: latest | tag | anchor | range | highest.
	Release(repo string, d version.Decl) (*Release, error)
	Releases(repo string) ([]*Release, error)
	ChildDependencies(repo, tag string) (*ojson.OMap, error)
	Download(a *Asset) ([]byte, error)
	// ArchiveURL is the URL recorded in metadata for a release asset.
	ArchiveURL(a *Asset) string
}

// HTTP is the shared bounded, retrying transport.
type HTTP struct {
	Token    string
	OfflineM bool
	Attempts int
	Timeout  time.Duration
	Client   *http.Client
	Sleep    func(time.Duration)
	Now      func() time.Time
	Jitter   func() float64
	// TokenHost is the only host (name and port) that receives the token.
	TokenHost string
	// TokenHeader builds the credential header.
	TokenHeader func(token string) (string, string)
	Extra       func(h http.Header)
	NameStr     string
	MaxBytes    int64
}

func (h *HTTP) defaults() {
	if h.Attempts == 0 {
		h.Attempts = 4
	}
	if h.Timeout == 0 {
		h.Timeout = 60 * time.Second
	}
	if h.Sleep == nil {
		h.Sleep = time.Sleep
	}
	if h.Now == nil {
		h.Now = time.Now
	}
	if h.Jitter == nil {
		h.Jitter = rand.Float64
	}
	if h.MaxBytes == 0 {
		h.MaxBytes = MaxResponse
	}
	if h.Client == nil {
		h.Client = &http.Client{}
	}
}

func (h *HTTP) hint() string {
	if h.Token != "" {
		return ""
	}
	return "; a token raises the limit"
}

func (h *HTTP) headers(u, accept string) http.Header {
	hd := http.Header{}
	hd.Set("Accept", accept)
	hd.Set("User-Agent", UserAgent)
	if h.Extra != nil {
		h.Extra(hd)
	}
	if h.Token != "" {
		if parsed, err := url.Parse(u); err == nil && strings.EqualFold(parsed.Host, h.TokenHost) && h.TokenHeader != nil {
			k, v := h.TokenHeader(h.Token)
			hd.Set(k, v)
		}
	}
	return hd
}

func retryAfter(v string, now time.Time) float64 {
	if v == "" {
		return 0
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	if t, err := http.ParseTime(v); err == nil {
		return math.Max(0, t.Sub(now).Seconds())
	}
	return 0
}

// Request fetches a URL with bounded retries. 404 yields ErrNotFound.
func (h *HTTP) Request(rawURL, accept string) ([]byte, error) {
	h.defaults()
	if h.OfflineM {
		return nil, failf("offline: network access disabled")
	}
	// Redirects are followed by hand so that the credential is re-evaluated for every hop.
	client := *h.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Timeout = h.Timeout
	var last string
	for attempt := 0; attempt < h.Attempts; attempt++ {
		delay := 0.0
		current := rawURL
		var status int
		var resp *http.Response
		var netErr error
		hops := 0
		for {
			req, err := http.NewRequest(http.MethodGet, current, nil)
			if err != nil {
				return nil, failf("invalid URL %q", current)
			}
			req.Header = h.headers(current, accept)
			resp, netErr = client.Do(req)
			if netErr != nil {
				break
			}
			if resp.StatusCode/100 == 3 && resp.Header.Get("Location") != "" {
				loc := resp.Header.Get("Location")
				resp.Body.Close()
				hops++
				if hops >= maxRedirects {
					return nil, failf("too many redirects")
				}
				next, err := req.URL.Parse(loc)
				if err != nil {
					return nil, failf("invalid redirect from %s", req.URL.Redacted())
				}
				current = next.String()
				continue
			}
			break
		}
		if netErr != nil {
			last = netErr.Error()
			delay = math.Min(math.Pow(2, float64(attempt))+h.Jitter(), 20)
		} else {
			status = resp.StatusCode
			if status == 200 {
				if resp.ContentLength > h.MaxBytes {
					resp.Body.Close()
					return nil, failf("response too large")
				}
				data, err := io.ReadAll(io.LimitReader(resp.Body, h.MaxBytes+1))
				resp.Body.Close()
				if err != nil {
					last = err.Error()
					delay = math.Min(math.Pow(2, float64(attempt))+h.Jitter(), 20)
					goto wait
				}
				if int64(len(data)) > h.MaxBytes {
					return nil, failf("response too large")
				}
				return data, nil
			}
			if status/100 == 2 {
				// Other 2xx are not expected from these APIs; treat as success with the body.
				data, _ := io.ReadAll(io.LimitReader(resp.Body, h.MaxBytes+1))
				resp.Body.Close()
				return data, nil
			}
			resp.Body.Close()
			last = fmt.Sprintf("HTTP %d", status)
			switch {
			case (status == 403 || status == 429) && resp.Header.Get("X-RateLimit-Remaining") == "0":
				reset, _ := strconv.ParseFloat(resp.Header.Get("X-RateLimit-Reset"), 64)
				wait := reset - float64(h.Now().Unix())
				if wait > maxRetryDelay {
					return nil, failf("%s API rate limit exhausted (HTTP %d)%s", h.NameStr, status, h.hint())
				}
				delay = math.Max(1, wait)
			case status == 429 || status == 500 || status == 502 || status == 503 || status == 504:
				delay = retryAfter(resp.Header.Get("Retry-After"), h.Now())
				if delay == 0 {
					delay = math.Min(math.Pow(2, float64(attempt))+h.Jitter(), 20)
				}
				if delay > maxRetryDelay {
					// gitlab.com answers unauthenticated bursts with a Retry-After of many minutes; 4D does
					// not wait either. Fail now with a clear reason.
					return nil, failf("%s rate limit (HTTP %d); retry after %d s%s", h.NameStr, status, int(delay), h.hint())
				}
			case status == 401 || status == 403:
				msg := "access denied"
				if h.Token != "" {
					msg = "token rejected or not authorised for this resource (no anonymous retry, as 4D)"
				}
				return nil, &AuthFailure{Failure{Msg: fmt.Sprintf("HTTP %d from %s: %s", status, h.NameStr, msg)}}
			case status == 404:
				return nil, ErrNotFound
			default:
				return nil, failf("HTTP %d for %s", status, rawURL)
			}
		}
	wait:
		if attempt+1 < h.Attempts {
			h.Sleep(time.Duration(delay * float64(time.Second)))
		}
	}
	return nil, failf("network error for %s: %s", rawURL, last)
}

// Quote percent-encodes everything but RFC 3986 unreserved characters (Python's quote(s, safe="")).
func Quote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func str(v any) string { s, _ := v.(string); return s }

func parseAsset(o *ojson.OMap) *Asset {
	a := &Asset{Name: str(o.Val("name")), ID: o.Val("id"), Digest: str(o.Val("digest")),
		URL: str(o.Val("url")), BrowserDownloadURL: str(o.Val("browser_download_url"))}
	if n, ok := o.Val("size").(interface{ Int64() (int64, error) }); ok {
		if v, err := n.Int64(); err == nil {
			a.Size = &v
		}
	}
	return a
}

func parseRelease(v any) (*Release, error) {
	o, ok := v.(*ojson.OMap)
	if !ok {
		return nil, failf("unexpected release payload")
	}
	r := &Release{Tag: str(o.Val("tag_name")), HTMLURL: str(o.Val("html_url"))}
	if r.Tag == "" {
		return nil, failf("release without tag_name")
	}
	if list, ok := o.Val("assets").([]any); ok {
		for _, item := range list {
			if ao, ok := item.(*ojson.OMap); ok {
				r.Assets = append(r.Assets, parseAsset(ao))
			}
		}
	}
	return r, nil
}

func parseJSON(data []byte) (any, error) {
	v, err := ojson.Parse(data)
	if err != nil {
		return nil, failf("invalid JSON from the provider: %v", err)
	}
	return v, nil
}

// ManifestDependencies is the `dependencies` object of a dependencies.json document. A nil result means none.
func ManifestDependencies(v any) (*ojson.OMap, error) {
	top, ok := v.(*ojson.OMap)
	if !ok {
		return nil, errors.New("the top level must be a JSON object")
	}
	d, present := top.Get("dependencies")
	if !present {
		return ojson.New(), nil
	}
	dm, ok := d.(*ojson.OMap)
	if !ok {
		return nil, errors.New(`"dependencies" must be a JSON object`)
	}
	return dm, nil
}

func (a *AuthFailure) Unwrap() error { return &a.Failure }
func (n *NotFound) Unwrap() error    { return &n.Failure }
