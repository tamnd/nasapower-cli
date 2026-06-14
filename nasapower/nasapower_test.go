package nasapower_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tamnd/nasapower-cli/nasapower"
)

// sampleResponse is the JSON shape the NASA POWER API returns for a
// temporal/point request.
const sampleDailyResponse = `{
  "geometry": {
    "type": "Point",
    "coordinates": [-74.01, 40.71, 0.0]
  },
  "properties": {
    "parameter": {
      "T2M": {
        "20230101": 1.23,
        "20230102": 2.45,
        "20230103": 3.67
      },
      "PRECTOTCORR": {
        "20230101": 0.0,
        "20230102": 1.2,
        "20230103": 0.0
      }
    }
  }
}`

const sampleMonthlyResponse = `{
  "geometry": {
    "type": "Point",
    "coordinates": [-74.01, 40.71, 0.0]
  },
  "properties": {
    "parameter": {
      "T2M": {
        "202001": -1.5,
        "202002": 0.3,
        "202003": 5.8
      },
      "ALLSKY_SFC_SW_DWN": {
        "202001": 1.12,
        "202002": 2.34,
        "202003": 3.56
      }
    }
  }
}`

func newTestServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("request carried no User-Agent")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func fastClient(baseURL string) *nasapower.Client {
	c := nasapower.NewClient()
	c.Rate = 0 // no pacing in tests
	// Override the HTTP client to point at our test server via a custom transport.
	// We do this by using the client as-is; the caller passes full URLs.
	return c
}

// TestGetUserAgent verifies the client sets a User-Agent on every request.
func TestGetUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	c := nasapower.NewClient()
	c.Rate = 0
	_, _ = c.Get(context.Background(), srv.URL)

	if gotUA == "" {
		t.Error("request carried no User-Agent")
	}
	if gotUA != nasapower.DefaultUserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, nasapower.DefaultUserAgent)
	}
}

// TestGetRetriesOn503 verifies that transient 503 errors trigger retries with
// backoff, and that the client eventually succeeds.
func TestGetRetriesOn503(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("recovered"))
	}))
	defer srv.Close()

	c := nasapower.NewClient()
	c.Rate = 0
	c.Retries = 5

	start := time.Now()
	body, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "recovered" {
		t.Errorf("body = %q after retries", body)
	}
	if hits != 3 {
		t.Errorf("server saw %d hits, want 3", hits)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Error("retries did not back off")
	}
}

// TestGetFailsOn404 verifies that non-retryable errors (4xx != 429) are
// returned immediately without further retries.
func TestGetFailsOn404(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := nasapower.NewClient()
	c.Rate = 0
	c.Retries = 5

	_, err := c.Get(context.Background(), srv.URL)
	if err == nil {
		t.Error("expected error on 404, got nil")
	}
	if hits != 1 {
		t.Errorf("server saw %d hits, want 1 (no retry on 404)", hits)
	}
}

// TestDaily decodes a sample NASA POWER daily response and verifies that the
// correct number of Observations are returned with the right field values.
func TestDaily(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify the request hits the right path.
		if r.URL.Path != "/api/temporal/daily/point" {
			t.Errorf("path = %q, want /api/temporal/daily/point", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("format") != "JSON" {
			t.Errorf("format = %q, want JSON", q.Get("format"))
		}
		if q.Get("community") != "SB" {
			t.Errorf("community = %q, want SB", q.Get("community"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleDailyResponse))
	}))
	defer srv.Close()

	c := nasapower.NewClient()
	c.Rate = 0
	// Redirect to test server by replacing the base URL in the request.
	// We use a custom RoundTripper to intercept and rewrite the host.
	c.HTTP = &http.Client{
		Transport: rewriteHost(srv.URL),
	}

	obs, err := c.Daily(context.Background(), 40.71, -74.01, "20230101", "20230103", "T2M,PRECTOTCORR", "SB")
	if err != nil {
		t.Fatalf("Daily: %v", err)
	}

	// 2 parameters × 3 dates = 6 observations
	if len(obs) != 6 {
		t.Errorf("len(obs) = %d, want 6", len(obs))
	}

	// Observations are sorted by date then parameter.
	// First: 20230101/PRECTOTCORR, 20230101/T2M, 20230102/PRECTOTCORR, ...
	for _, o := range obs {
		if o.Lat != 40.71 {
			t.Errorf("Lat = %v, want 40.71", o.Lat)
		}
		if o.Lon != -74.01 {
			t.Errorf("Lon = %v, want -74.01", o.Lon)
		}
		if o.Parameter == "" {
			t.Error("Parameter is empty")
		}
		if o.Date == "" {
			t.Error("Date is empty")
		}
	}

	// Spot-check: find T2M for 20230101, value should be 1.23.
	var found bool
	for _, o := range obs {
		if o.Date == "20230101" && o.Parameter == "T2M" {
			found = true
			if o.Value != 1.23 {
				t.Errorf("T2M 20230101 = %v, want 1.23", o.Value)
			}
		}
	}
	if !found {
		t.Error("T2M for 20230101 not found in observations")
	}
}

// TestMonthly decodes a sample NASA POWER monthly response.
func TestMonthly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/temporal/monthly/point" {
			t.Errorf("path = %q, want /api/temporal/monthly/point", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleMonthlyResponse))
	}))
	defer srv.Close()

	c := nasapower.NewClient()
	c.Rate = 0
	c.HTTP = &http.Client{Transport: rewriteHost(srv.URL)}

	obs, err := c.Monthly(context.Background(), 40.71, -74.01, "2020", "2020", "T2M,ALLSKY_SFC_SW_DWN", "RE")
	if err != nil {
		t.Fatalf("Monthly: %v", err)
	}

	// 2 parameters × 3 months = 6 observations
	if len(obs) != 6 {
		t.Errorf("len(obs) = %d, want 6", len(obs))
	}

	// Spot-check: ALLSKY_SFC_SW_DWN for 202003 should be 3.56.
	for _, o := range obs {
		if o.Date == "202003" && o.Parameter == "ALLSKY_SFC_SW_DWN" {
			if o.Value != 3.56 {
				t.Errorf("ALLSKY_SFC_SW_DWN 202003 = %v, want 3.56", o.Value)
			}
			return
		}
	}
	t.Error("ALLSKY_SFC_SW_DWN for 202003 not found")
}

// TestObservationJSON verifies that Observation serialises cleanly to JSON
// with the expected field names.
func TestObservationJSON(t *testing.T) {
	o := nasapower.Observation{
		Lat:       40.71,
		Lon:       -74.01,
		Date:      "20230101",
		Parameter: "T2M",
		Value:     1.23,
	}
	b, err := json.Marshal(o)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"lat", "lon", "date", "parameter", "value"} {
		if _, ok := m[key]; !ok {
			t.Errorf("JSON missing key %q", key)
		}
	}
}

// rewriteHost returns an http.RoundTripper that replaces the scheme+host of
// every outbound request with the given base URL (e.g. "http://127.0.0.1:PORT").
// This lets us point the production Client at a local test server without
// modifying the Client's base-URL constant.
func rewriteHost(baseURL string) http.RoundTripper {
	return &hostRewriter{base: baseURL, inner: http.DefaultTransport}
}

type hostRewriter struct {
	base  string
	inner http.RoundTripper
}

func (h *hostRewriter) RoundTrip(req *http.Request) (*http.Response, error) {
	r2 := req.Clone(req.Context())
	r2.URL.Scheme = "http"
	// Parse the test server host from baseURL.
	u, _ := http.NewRequest("GET", h.base, nil)
	r2.URL.Host = u.URL.Host
	return h.inner.RoundTrip(r2)
}
