// Package version implements the tag, range and runtime-anchor semantics of the 4D dependency manager as
// observed in real 4D (see docs/component-manager-behavior.md). It deliberately is not a generic SemVer
// solver: R-release tags are not SemVer and never satisfy a range, and "4d" anchors follow the observed
// LTS/R-train rules.
package version

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Failure is a dependency-level error: it makes one component incomplete instead of stopping the run.
type Failure struct{ Msg string }

func (f *Failure) Error() string { return f.Msg }

func failf(format string, args ...any) error { return &Failure{Msg: fmt.Sprintf(format, args...)} }

// UsageError is an invalid invocation or an invalid runtime version (exit 2).
type UsageError struct{ Msg string }

func (u *UsageError) Error() string { return u.Msg }

var (
	tagRe     = regexp.MustCompile(`^v?(\d+)(?:R(\d+))?(?:\.(\d+))?(?:\.(\d+))?$`)
	anchorRe  = regexp.MustCompile(`^(\d+)(?:R(\d+))?$`)
	runtimeRe = regexp.MustCompile(`^\s*(\d+)(?:\s*R(\d+))?(?:\.(\d+))?`)
	partialRe = regexp.MustCompile(`^v?(\d+|[xX*])(?:\.(\d+|[xX*]))?(?:\.(\d+|[xX*]))?$`)
	semverRe  = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?(?:\.(\d+))?$`)
	hyphenRe  = regexp.MustCompile(`^(\S+)\s+-\s+(\S+)$`)
	tokenRe   = regexp.MustCompile(`(?:>=|<=|>|<|=|\^|~)?\s*[^\s<>=^~]+`)
	compRe    = regexp.MustCompile(`^(>=|<=|>|<|=|\^|~)?\s*(.+)$`)
)

// Key is the ordering key of a 4D tag: (major, R release, a, b). LTS tags have R = 0.
type Key [4]int

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// TagKey parses a 4D or plain version tag such as 21.5, v1.2.3, 21R5 or 21R5.1.
func TagKey(tag string) (Key, bool) {
	m := tagRe.FindStringSubmatch(tag)
	if m == nil {
		return Key{}, false
	}
	return Key{atoi(m[1]), atoi(m[2]), atoi(m[3]), atoi(m[4])}, true
}

func (k Key) less(o Key) bool {
	for i := range k {
		if k[i] != o[i] {
			return k[i] < o[i]
		}
	}
	return false
}

// RuntimeAnchor maps a runtime version to the anchor and numeric version code: "21R5.1" -> ("21R5", 2150),
// "21.2" -> ("21", 2102), "21" -> ("21", 2100).
func RuntimeAnchor(v string) (string, int, error) {
	m := runtimeRe.FindStringSubmatch(v)
	if m == nil {
		return "", 0, &UsageError{Msg: fmt.Sprintf("unrecognised runtime version %q", v)}
	}
	major, minor := atoi(m[1]), atoi(m[3])
	if m[2] != "" {
		r := atoi(m[2])
		return fmt.Sprintf("%dR%d", major, r), major*100 + r*10, nil
	}
	if minor > 99 {
		minor = 99
	}
	return strconv.Itoa(major), major*100 + minor, nil
}

// AnchorPick selects for 4D:N the newest N.x tag (never an R tag), else the newest older tag; for 4D:NRm the
// newest N.x or NRk tag with k <= m, else the newest older tag. It returns "" when nothing qualifies.
func AnchorPick(tags []string, anchor string) (string, error) {
	m := anchorRe.FindStringSubmatch(anchor)
	if m == nil {
		return "", failf("invalid 4D anchor %q", anchor)
	}
	major := atoi(m[1])
	type keyed struct {
		k Key
		t string
	}
	var all, pool []keyed
	for _, t := range tags {
		if k, ok := TagKey(t); ok {
			all = append(all, keyed{k, t})
		}
	}
	for _, x := range all {
		if m[2] == "" {
			if x.k[0] == major && x.k[1] == 0 {
				pool = append(pool, x)
			}
		} else if x.k[0] == major && x.k[1] <= atoi(m[2]) {
			pool = append(pool, x)
		}
	}
	if len(pool) == 0 {
		for _, x := range all {
			if x.k[0] < major {
				pool = append(pool, x)
			}
		}
	}
	if len(pool) == 0 {
		return "", nil
	}
	best := pool[0]
	for _, x := range pool[1:] {
		// Python's max() over (key, tag) tuples: the key decides, the tag text breaks ties.
		if best.k.less(x.k) || (best.k == x.k && best.t < x.t) {
			best = x
		}
	}
	return best.t, nil
}

// AnchorAllows reports whether a 4D:<anchor> declaration can resolve to tag: the anchor's own major (and R
// releases up to it) or any older major (observed case A2).
func AnchorAllows(tag, anchor string) bool {
	m := anchorRe.FindStringSubmatch(anchor)
	k, ok := TagKey(tag)
	if m == nil || !ok {
		return false
	}
	major := atoi(m[1])
	if k[0] < major {
		return true
	}
	if k[0] != major {
		return false
	}
	if m[2] == "" {
		return k[1] == 0
	}
	return k[1] <= atoi(m[2])
}

// Semver returns the numeric key of a tag for range matching. R tags are not SemVer.
func Semver(tag string) ([3]int, bool) {
	m := semverRe.FindStringSubmatch(tag)
	if m == nil {
		return [3]int{}, false
	}
	return [3]int{atoi(m[1]), atoi(m[2]), atoi(m[3])}, true
}

// Constraint is one comparison against a (major, minor, patch) bound. Unbounded is the reference
// implementation's quirk for "a - *": an upper bound that compares as true.
type Constraint struct {
	Op        string
	Bound     [3]int
	Unbounded bool
}

// Ranges is an OR of AND-ed constraint sets.
type Ranges [][]Constraint

func partial(text string) ([]int, error) {
	m := partialRe.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return nil, failf("invalid version %q", text)
	}
	parts := []int{}
	for _, g := range m[1:] {
		if g == "" || g == "x" || g == "X" || g == "*" {
			break
		}
		parts = append(parts, atoi(g))
	}
	return parts, nil
}

func pad(parts []int) [3]int {
	var out [3]int
	copy(out[:], parts)
	return out
}

// bump is the exclusive upper bound of an X-range such as 1 or 1.2; ok is false for an empty range.
func bump(parts []int) ([3]int, bool) {
	if len(parts) == 0 {
		return [3]int{}, false
	}
	up := append(append([]int{}, parts[:len(parts)-1]...), parts[len(parts)-1]+1)
	return pad(up), true
}

func comparator(token string) ([]Constraint, error) {
	m := compRe.FindStringSubmatch(token)
	op, text := m[1], m[2]
	parts, err := partial(text)
	if err != nil {
		return nil, err
	}
	low := pad(parts)
	switch op {
	case "", "=":
		if len(parts) == 3 {
			return []Constraint{{Op: "=", Bound: low}}, nil
		}
		out := []Constraint{{Op: ">=", Bound: low}}
		if up, ok := bump(parts); ok {
			out = append(out, Constraint{Op: "<", Bound: up})
		}
		return out, nil
	case "^":
		p := pad(parts)
		var up [3]int
		switch {
		case p[0] > 0 || len(parts) == 1:
			up = [3]int{p[0] + 1, 0, 0}
		case p[1] > 0 || len(parts) == 2:
			up = [3]int{0, p[1] + 1, 0}
		default:
			up = [3]int{0, 0, p[2] + 1}
		}
		return []Constraint{{Op: ">=", Bound: low}, {Op: "<", Bound: up}}, nil
	case "~":
		out := []Constraint{{Op: ">=", Bound: low}}
		head := parts
		if len(head) > 2 {
			head = head[:2]
		}
		if up, ok := bump(head); ok {
			out = append(out, Constraint{Op: "<", Bound: up})
		}
		return out, nil
	case ">":
		if len(parts) < 3 {
			if up, ok := bump(parts); ok {
				return []Constraint{{Op: ">=", Bound: up}}, nil
			}
			return []Constraint{{Op: "<", Bound: [3]int{}}}, nil
		}
	case "<=":
		if len(parts) < 3 {
			if up, ok := bump(parts); ok {
				return []Constraint{{Op: "<", Bound: up}}, nil
			}
			return nil, nil
		}
	}
	return []Constraint{{Op: op, Bound: low}}, nil
}

// ParseRange parses the documented range syntax: *, 1.*, 1.2.*, >= > <= < =, ^, ~, "a - b", space separated AND,
// and || OR.
func ParseRange(spec string) (Ranges, error) {
	var sets Ranges
	for _, alt := range strings.Split(spec, "||") {
		alt = strings.TrimSpace(alt)
		alt = strings.ReplaceAll(alt, "–", "-")
		var cs []Constraint
		if m := hyphenRe.FindStringSubmatch(alt); m != nil {
			a, err := partial(m[1])
			if err != nil {
				return nil, err
			}
			b, err := partial(m[2])
			if err != nil {
				return nil, err
			}
			cs = append(cs, Constraint{Op: ">=", Bound: pad(a)})
			if len(b) == 3 {
				cs = append(cs, Constraint{Op: "<=", Bound: pad(b)})
			} else if up, ok := bump(b); ok {
				cs = append(cs, Constraint{Op: "<", Bound: up})
			} else {
				cs = append(cs, Constraint{Op: "<", Unbounded: true})
			}
		} else if alt != "" && alt != "*" && alt != "x" && alt != "X" {
			for _, tok := range tokenRe.FindAllString(alt, -1) {
				c, err := comparator(strings.TrimSpace(tok))
				if err != nil {
					return nil, err
				}
				cs = append(cs, c...)
			}
		}
		sets = append(sets, cs)
	}
	return sets, nil
}

func cmp3(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Satisfies reports whether tag is matched by the ranges. A tag that is not plain major[.minor[.patch]] never is.
func Satisfies(tag string, r Ranges) bool {
	v, ok := Semver(tag)
	if !ok {
		return false
	}
	for _, set := range r {
		all := true
		for _, c := range set {
			if c.Unbounded {
				continue
			}
			d := cmp3(v, c.Bound)
			var pass bool
			switch c.Op {
			case ">=":
				pass = d >= 0
			case "<=":
				pass = d <= 0
			case ">":
				pass = d > 0
			case "<":
				pass = d < 0
			default:
				pass = d == 0
			}
			if !pass {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// MaxSemver returns the first tag with the highest Semver key (ties keep the earlier tag, as Python's max).
func MaxSemver(tags []string) string {
	best, found := "", false
	var bk [3]int
	for _, t := range tags {
		k, _ := Semver(t)
		if !found || cmp3(k, bk) > 0 {
			best, bk, found = t, k, true
		}
	}
	return best
}

// Kind names how a declaration selects a release.
const (
	KindTag     = "tag"
	KindLatest  = "latest"
	KindHighest = "highest"
	KindAnchor  = "anchor"
	KindRange   = "range"
)

// Decl is a classified declaration. Lock is the value recorded as the lock file's "version".
type Decl struct {
	Kind   string
	Tag    string
	Anchor string
	Ranges Ranges
	Lock   string
}

// Spec is the version-relevant part of a declaration. Tag is empty when absent or falsy; HasVersion is false
// when "version" is missing or null.
type Spec struct {
	Tag        string
	Version    string
	HasVersion bool
}

// Classify interprets a declaration. runtimeAnchor is the running runtime's anchor ("" when unknown). For GitLab,
// no version means "highest" (the highest semantic version) rather than "latest".
func Classify(s Spec, runtimeAnchor string, gitlab bool) (Decl, error) {
	if s.Tag != "" {
		return Decl{Kind: KindTag, Tag: s.Tag}, nil
	}
	v := strings.TrimSpace(s.Version)
	if !s.HasVersion || v == "" {
		if gitlab {
			return Decl{Kind: KindHighest, Lock: "highest"}, nil
		}
		return Decl{Kind: KindLatest, Lock: "latest"}, nil
	}
	switch {
	case gitlab && strings.EqualFold(v, "highest"):
		return Decl{Kind: KindHighest, Lock: v}, nil
	case strings.EqualFold(v, "latest"):
		return Decl{Kind: KindLatest, Lock: v}, nil
	case strings.EqualFold(v, "4d"):
		if runtimeAnchor == "" {
			return Decl{}, failf(`"version": "4d" needs the runtime version (--runtime-version)`)
		}
		return Decl{Kind: KindAnchor, Anchor: runtimeAnchor, Lock: "4D:" + runtimeAnchor}, nil
	case len(v) >= 3 && strings.EqualFold(v[:3], "4D:"):
		return Decl{Kind: KindAnchor, Anchor: v[3:], Lock: v}, nil
	case tagRe.MatchString(v):
		return Decl{Kind: KindTag, Tag: v, Lock: v}, nil
	}
	r, err := ParseRange(v)
	if err != nil {
		return Decl{}, err
	}
	return Decl{Kind: KindRange, Ranges: r, Lock: v}, nil
}

// Allows reports whether a declaration can resolve to tag (used to decide whether two declarations can meet).
func (d Decl) Allows(tag string) bool {
	switch d.Kind {
	case KindLatest:
		return true
	case KindTag:
		return tag == d.Tag
	case KindAnchor:
		return AnchorAllows(tag, d.Anchor)
	}
	return Satisfies(tag, d.Ranges)
}
