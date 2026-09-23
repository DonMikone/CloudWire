package updates

import "testing"

func TestCompare(t *testing.T) {
	ordered := []string{"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.10.0", "2.0.0"}
	for i := range len(ordered) - 1 {
		a, _ := Parse(ordered[i])
		b, _ := Parse(ordered[i+1])
		if Compare(a, b) >= 0 || Compare(b, a) <= 0 {
			t.Errorf("%s must be < %s", ordered[i], ordered[i+1])
		}
	}
	if _, ok := Parse("v1.2"); ok {
		t.Error("two-part version accepted")
	}
}

func TestNewestPrereleaseRule(t *testing.T) {
	rels := []Release{
		{TagName: "v0.3.0-rc.1", Prerelease: true, HTMLURL: "rc"},
		{TagName: "v0.2.0", HTMLURL: "stable"},
		{TagName: "v0.4.0", Draft: true, HTMLURL: "draft"},
		{TagName: "garbage"},
	}
	if r, ok := Newest(rels, "0.1.0"); !ok || r.HTMLURL != "rc" {
		t.Fatalf("0.x must see prereleases: %+v %v", r, ok)
	}
	if _, ok := Newest(rels, "0.3.0-rc.1"); ok {
		t.Fatal("same version reported as update")
	}
	rels = append(rels, Release{TagName: "v1.1.0-beta.1", Prerelease: true, HTMLURL: "beta"}, Release{TagName: "v1.0.1", HTMLURL: "patch"})
	if r, ok := Newest(rels, "1.0.0"); !ok || r.HTMLURL != "patch" {
		t.Fatalf("1.x must skip prereleases: %+v", r)
	}
	if _, ok := Newest(rels, "1.0.1"); ok {
		t.Fatal("no newer stable release expected")
	}
	if _, ok := Newest(rels, "0.0.0-dev"); !ok {
		t.Fatal("dev build must see releases")
	}
}
