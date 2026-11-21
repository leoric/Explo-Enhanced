package util

import "testing"

func TestCustomDirName(t *testing.T) {
	cases := map[string]string{
		"Top Weekly Videos Tamil": "Top Weekly Videos Tamil",
		"Rock/Pop: Hits?":         "Rock_Pop_ Hits_",
		"..":                      "Custom-42",
		"":                        "Custom-42",
	}
	for in, want := range cases {
		if got := CustomDirName(in, "custom-42"); got != want {
			t.Errorf("CustomDirName(%q) = %q, want %q", in, got, want)
		}
	}
}
