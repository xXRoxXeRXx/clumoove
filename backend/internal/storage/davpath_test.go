package storage

import "testing"

func TestEscapeDAVPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/Calibre-Bibliothek & Jugend/Sheila Burnford", "/Calibre-Bibliothek%20%26%20Jugend/Sheila%20Burnford"},
		{"Kinder & Jugend", "Kinder%20%26%20Jugend"},
		{"a+b", "a%2Bb"},
		{"normal/path", "normal/path"},
		{"file (1).txt", "file%20%281%29.txt"},
		{"ünïcode", "%C3%BCn%C3%AFcode"},
		{"a%b", "a%25b"},
		{"a?b", "a%3Fb"},
		{"a#b", "a%23b"},
		{"", ""},
		{"user@domain", "user%40domain"},
		{"user+tag", "user%2Btag"},
	}
	for _, c := range cases {
		if got := escapeDAVPath(c.in); got != c.want {
			t.Errorf("escapeDAVPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNextcloudPathsResourceURLEncodesSubDelims(t *testing.T) {
	np := nextcloudPaths{}
	got := np.resourceURL(
		"https://cloud.example/remote.php/dav",
		"user+tag",
		"files",
		"/Kinder & Jugend/file.txt",
	)
	want := "https://cloud.example/remote.php/dav/files/user%2Btag/Kinder%20%26%20Jugend/file.txt"
	if got != want {
		t.Errorf("resourceURL = %s, want %s", got, want)
	}

	gotUpload := np.uploadsURL(
		"https://cloud.example/remote.php/dav",
		"user@host",
		"/a+b/chunk",
	)
	wantUpload := "https://cloud.example/remote.php/dav/uploads/user%40host/a%2Bb/chunk"
	if gotUpload != wantUpload {
		t.Errorf("uploadsURL = %s, want %s", gotUpload, wantUpload)
	}
}

