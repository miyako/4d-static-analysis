package version

import "testing"

var tags = []string{"20.0.1", "20.0.3", "20.0.23", "20.1.0", "21.0", "21.5", "21R2", "21R5", "21R5.1", "21R6", "22.0"}

func pick(t *testing.T, spec string) string {
	t.Helper()
	r, err := ParseRange(spec)
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	for _, tag := range tags {
		if Satisfies(tag, r) {
			hits = append(hits, tag)
		}
	}
	if len(hits) == 0 {
		return ""
	}
	return MaxSemver(hits)
}

// These cases mirror the reference implementation's Versions test class.
func TestRanges(t *testing.T) {
	for spec, want := range map[string]string{
		"^20.0.0": "20.1.0", "~20.0.0": "20.0.23", "20.0.*": "20.0.23", "20.*": "20.1.0",
		">=20.0.3 <20.0.23": "20.0.3", "20.0.1 - 20.0.3": "20.0.3", "20.0.1 – 20.0.3": "20.0.3",
		"<20.0.3 || >=22": "22.0", "*": "22.0", ">21.5": "22.0", "^23.0.0": "",
	} {
		if got := pick(t, spec); got != want {
			t.Errorf("%q: got %q want %q", spec, got, want)
		}
	}
}

func TestInvalidRange(t *testing.T) {
	for _, spec := range []string{"abc", "1.2.3.4", ">=x.y"} {
		if _, err := ParseRange(spec); err == nil {
			t.Errorf("%q must be rejected", spec)
		}
	}
}

func TestAnchors(t *testing.T) {
	cases := []struct {
		version, anchor string
		code            int
	}{
		{"21R5", "21R5", 2150}, {"21R5.1", "21R5", 2150}, {"21.2", "21", 2102}, {"21", "21", 2100}, {"20 R7", "20R7", 2070},
	}
	for _, c := range cases {
		a, code, err := RuntimeAnchor(c.version)
		if err != nil || a != c.anchor || code != c.code {
			t.Errorf("%q: %q %d %v", c.version, a, code, err)
		}
	}
	if _, _, err := RuntimeAnchor("x"); err == nil {
		t.Error("unrecognised version must fail")
	}
	for anchor, want := range map[string]string{"21R5": "21R5.1", "21": "21.5", "21R2": "21R2", "22": "22.0", "19": ""} {
		got, err := AnchorPick(tags, anchor)
		if err != nil || got != want {
			t.Errorf("anchor %s: %q %v want %q", anchor, got, err, want)
		}
	}
	if !AnchorAllows("20.0.1", "21") || AnchorAllows("21R2", "21") || !AnchorAllows("21R2", "21R5") || AnchorAllows("22.0", "21R5") {
		t.Error("AnchorAllows")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		s      Spec
		gitlab bool
		kind   string
		lock   string
	}{
		{Spec{Tag: "v1.0"}, false, KindTag, ""},
		{Spec{}, false, KindLatest, "latest"},
		{Spec{}, true, KindHighest, "highest"},
		{Spec{Version: "Latest", HasVersion: true}, true, KindLatest, "Latest"},
		{Spec{Version: "4d", HasVersion: true}, false, KindAnchor, "4D:21R5"},
		{Spec{Version: "4D:21", HasVersion: true}, false, KindAnchor, "4D:21"},
		{Spec{Version: "1.2.3", HasVersion: true}, false, KindTag, "1.2.3"},
		{Spec{Version: "^1.2", HasVersion: true}, false, KindRange, "^1.2"},
		{Spec{Version: "  ", HasVersion: true}, false, KindLatest, "latest"},
	}
	for _, c := range cases {
		d, err := Classify(c.s, "21R5", c.gitlab)
		if err != nil || d.Kind != c.kind || d.Lock != c.lock {
			t.Errorf("%+v: %+v %v", c.s, d, err)
		}
	}
	if _, err := Classify(Spec{Version: "4d", HasVersion: true}, "", false); err == nil {
		t.Error("4d without a runtime version must fail")
	}
}
