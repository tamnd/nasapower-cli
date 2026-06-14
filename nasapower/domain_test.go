package nasapower

import (
	"testing"

	"github.com/tamnd/any-cli/kit"
)

// These tests are offline: they exercise the URI driver's pure string
// functions and the host wiring, which need no network.

func TestDomainInfo(t *testing.T) {
	info := Domain{}.Info()
	if info.Scheme != "nasapower" {
		t.Errorf("Scheme = %q, want nasapower", info.Scheme)
	}
	if len(info.Hosts) == 0 || info.Hosts[0] != Host {
		t.Errorf("Hosts = %v, want [%s]", info.Hosts, Host)
	}
	if info.Identity.Binary != "nasapower" {
		t.Errorf("Identity.Binary = %q, want nasapower", info.Identity.Binary)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		in  string
		typ string
		id  string
		ok  bool
	}{
		{"40.71,-74.01", "point", "40.71,-74.01", true},
		{"51.5,-0.12", "point", "51.5,-0.12", true},
		{"-33.86,151.21", "point", "-33.86,151.21", true},
		{"20230101", "date", "20230101", true},
		{"202301", "date", "202301", true},
		{"2023", "date", "2023", true},
		{"not-a-ref", "", "", false},
		{"about/page", "", "", false},
	}
	for _, tc := range cases {
		typ, id, err := Domain{}.Classify(tc.in)
		if tc.ok {
			if err != nil {
				t.Errorf("Classify(%q) error = %v, want nil", tc.in, err)
				continue
			}
			if typ != tc.typ || id != tc.id {
				t.Errorf("Classify(%q) = (%q, %q), want (%q, %q)", tc.in, typ, id, tc.typ, tc.id)
			}
		} else {
			if err == nil {
				t.Errorf("Classify(%q) = (%q, %q, nil), want error", tc.in, typ, id)
			}
		}
	}
}

func TestLocate(t *testing.T) {
	cases := []struct {
		uriType string
		id      string
		want    string
		ok      bool
	}{
		{"point", "40.71,-74.01", "https://power.larc.nasa.gov/data-access-viewer/", true},
		{"date", "20230101", "https://power.larc.nasa.gov/data-access-viewer/", true},
		{"unknown", "foo", "", false},
	}
	for _, tc := range cases {
		got, err := Domain{}.Locate(tc.uriType, tc.id)
		if tc.ok {
			if err != nil || got != tc.want {
				t.Errorf("Locate(%q, %q) = (%q, %v), want (%q, nil)", tc.uriType, tc.id, got, err, tc.want)
			}
		} else {
			if err == nil {
				t.Errorf("Locate(%q, %q) = (%q, nil), want error", tc.uriType, tc.id, got)
			}
		}
	}
}

func TestIsDateLike(t *testing.T) {
	cases := []struct {
		s  string
		ok bool
	}{
		{"2023", true},
		{"202301", true},
		{"20230101", true},
		{"2023013", false},  // 7 digits — invalid
		{"abcdefgh", false}, // letters
		{"2023-01", false},  // hyphen
		{"", false},
	}
	for _, tc := range cases {
		got := isDateLike(tc.s)
		if got != tc.ok {
			t.Errorf("isDateLike(%q) = %v, want %v", tc.s, got, tc.ok)
		}
	}
}

// TestHostWiring mounts the driver in a kit Host and checks that the nasapower
// domain is registered and can resolve coordinate inputs.
func TestHostWiring(t *testing.T) {
	h, err := kit.Open()
	if err != nil {
		t.Fatal(err)
	}

	// The nasapower domain should be among the registered schemes.
	found := false
	for _, s := range h.Domains() {
		if s == "nasapower" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("nasapower domain not registered; got %v", h.Domains())
	}

	// ResolveOn a coordinate pair should produce a nasapower:// URI.
	got, err := h.ResolveOn("nasapower", "40.71,-74.01")
	if err != nil {
		t.Fatalf("ResolveOn: %v", err)
	}
	if got.Scheme != "nasapower" {
		t.Errorf("ResolveOn scheme = %q, want nasapower", got.Scheme)
	}
	if got.Authority != "point" {
		t.Errorf("ResolveOn authority = %q, want point", got.Authority)
	}

	// ResolveOn a date string should also work.
	gotDate, err := h.ResolveOn("nasapower", "20230101")
	if err != nil {
		t.Fatalf("ResolveOn date: %v", err)
	}
	if gotDate.Scheme != "nasapower" {
		t.Errorf("ResolveOn date scheme = %q, want nasapower", gotDate.Scheme)
	}
	if gotDate.Authority != "date" {
		t.Errorf("ResolveOn date authority = %q, want date", gotDate.Authority)
	}
}
