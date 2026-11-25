package ojson

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOrderAndDuplicates(t *testing.T) {
	v, err := Parse([]byte(`{"b": 1, "a": {"x": [1, 2.0]}, "b": 3}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Marshal(v, "")
	if string(got) != `{"b": 3, "a": {"x": [1, 2.0]}}` {
		t.Fatalf("got %s", got)
	}
}

func TestIndentMatchesPython(t *testing.T) {
	v, _ := Parse([]byte(`{"a": [], "b": {}, "c": [1, {"d": null}]}`))
	got, _ := Marshal(v, "  ")
	want := "{\n  \"a\": [],\n  \"b\": {},\n  \"c\": [\n    1,\n    {\n      \"d\": null\n    }\n  ]\n}"
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEscapes(t *testing.T) {
	bs := "\\"
	want := "\"" + bs + "u00e9" + bs + "u2028" + bs + "ud83d" + bs + "ude00" + bs + "\"" + bs + "n\""
	if got := PyDumps("\u00e9\u2028\U0001F600\"\n"); got != want {
		t.Fatalf("ascii %q want %q", got, want)
	}
	b, _ := Marshal("\u00e9\"<>&/", "")
	if string(b) != "\"\u00e9"+bs+"\"<>&/\"" {
		t.Fatalf("utf8 %s", b)
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{"", "{", `{"a":}`, `{} {}`, `[1,]`, `{1: 2}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestSemantics(t *testing.T) {
	a, _ := Parse([]byte(`{"a": 1, "b": [1, 2]}`))
	b, _ := Parse([]byte(`{"b": [1, 2.0], "a": 1.0}`))
	if !Equal(a, b) {
		t.Fatal("python equality ignores key order and 1 == 1.0")
	}
	for _, c := range []struct {
		v    any
		want bool
	}{{nil, false}, {"", false}, {"x", true}, {json.Number("0"), false}, {json.Number("2"), true}, {[]any{}, false}, {New(), false}} {
		if Truthy(c.v) != c.want {
			t.Errorf("Truthy(%v)", c.v)
		}
	}
	if Str(true) != "True" || Str(nil) != "None" || Str(json.Number("21.0")) != "21.0" {
		t.Fatal("Str")
	}
	if !strings.HasPrefix(string(StripBOM([]byte("\xEF\xBB\xBF{}"))), "{") {
		t.Fatal("BOM")
	}
}
