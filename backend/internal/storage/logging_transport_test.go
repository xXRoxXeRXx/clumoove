package storage

import "testing"

func TestHostCategoryFromURL(t *testing.T) {
	cases := map[string]string{
		"127.0.0.1": "loopback",
		"10.0.0.1":  "private",
		"8.8.8.8":   "public",
		"server":    "hostname",
	}
	for host, want := range cases {
		if got := hostCategoryFromURL(host); got != want {
			t.Errorf("hostCategoryFromURL(%q) = %q, want %q", host, got, want)
		}
	}
}
