package staticdeploy

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestValidSiteName(t *testing.T) {
	for _, name := range []string{"a", "site-01", strings.Repeat("a", 63)} {
		if !validName(name) {
			t.Errorf("valid site name rejected: %q", name)
		}
	}
	for _, name := range []string{"", "Upper", "-site", "site-", "site--two", "../site", "站点", strings.Repeat("a", 64)} {
		if validName(name) {
			t.Errorf("invalid site name accepted: %q", name)
		}
	}
}

func TestPublicationHostname(t *testing.T) {
	long := strings.Repeat("a", 63)
	maxHost := strings.Repeat(long+".", 3) + strings.Repeat("a", 61)
	for _, test := range []struct{ input, want string }{
		{"EXAMPLE.localhost.", "example.localhost"},
		{"xn--bcher-kva.example", "xn--bcher-kva.example"},
		{long + ".example", long + ".example"},
		{maxHost + ".", maxHost},
		{"", ""}, {"site..example", ""}, {"-site.example", ""}, {"site-.example", ""},
		{"site.example:80", ""}, {"https://site.example", ""}, {"site.example/", ""},
		{"站点.example", ""}, {"K.example", ""}, {long + "x.example", ""}, {strings.Repeat(long+".", 4) + "a", ""},
	} {
		got, err := publicationHostname(test.input)
		if got != test.want || (test.want == "" && !errors.Is(err, fs.ErrInvalid)) || (test.want != "" && err != nil) {
			t.Errorf("publicationHostname(%q) = (%q, %v), want %q", test.input, got, err, test.want)
		}
	}
}
