package main

import "testing"

func TestVersionRequested(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"--version"}, true},
		{[]string{"-version"}, true},
		{nil, false},
		{[]string{"--listen", ":8000"}, false},
		{[]string{"--version", "--listen", ":8000"}, false},
	} {
		if got := versionRequested(tc.args); got != tc.want {
			t.Errorf("versionRequested(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}
