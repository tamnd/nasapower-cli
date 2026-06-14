// Package nasapower is the library behind the nasapower command line:
// the HTTP client, request shaping, and the typed data models for
// the NASA POWER (Prediction Of Worldwide Energy Resources) API.
//
// The Client here is the spine every command shares. It sets a real
// User-Agent, paces requests so a busy session stays polite, and retries the
// transient failures (429 and 5xx) that any public API throws under load.
package nasapower

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// DefaultUserAgent identifies the client to the NASA POWER API.
const DefaultUserAgent = "nasapower/dev (+https://github.com/tamnd/nasapower-cli)"

// Host is the NASA POWER API host.
const Host = "power.larc.nasa.gov"

// BaseURL is the root every request is built from.
const BaseURL = "https://" + Host

// Client talks to the NASA POWER API over HTTP.
type Client struct {
	HTTP      *http.Client
	UserAgent string
	// Rate is the minimum gap between requests. Zero means no pacing.
	Rate    time.Duration
	Retries int

	last time.Time
}

// NewClient returns a Client with sensible defaults: a 30s timeout, a 500ms
// minimum gap between requests (NASA POWER recommends being polite), and
// five retries on transient errors.
func NewClient() *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		UserAgent: DefaultUserAgent,
		Rate:      500 * time.Millisecond,
		Retries:   5,
	}
}

// Observation is one data point from the NASA POWER API: a single parameter
// value for a single date at a single geographic coordinate.
type Observation struct {
	Lat       float64 `kit:"id" json:"lat"`
	Lon       float64 `json:"lon"`
	Date      string  `json:"date"`      // YYYYMMDD for daily, YYYYMM for monthly
	Parameter string  `json:"parameter"` // e.g. T2M, PRECTOTCORR
	Value     float64 `json:"value"`
}

// powerResponse mirrors the JSON shape returned by every NASA POWER
// temporal point endpoint.
type powerResponse struct {
	Geometry struct {
		Coordinates [3]float64 `json:"coordinates"` // [lon, lat, elev]
	} `json:"geometry"`
	Properties struct {
		Parameter map[string]map[string]float64 `json:"parameter"`
	} `json:"properties"`
}

// Daily fetches daily observations for the given latitude/longitude between
// start and end (both YYYYMMDD). params is a comma-separated list of NASA
// POWER parameter codes, e.g. "T2M,PRECTOTCORR,WS2M". community is one of
// RE, SB, or AG.
func (c *Client) Daily(ctx context.Context, lat, lon float64, start, end, params, community string) ([]Observation, error) {
	return c.fetch(ctx, "daily", lat, lon, start, end, params, community)
}

// Monthly fetches monthly observations for the given latitude/longitude
// between start and end (both YYYY). params and community are as in Daily.
func (c *Client) Monthly(ctx context.Context, lat, lon float64, start, end, params, community string) ([]Observation, error) {
	return c.fetch(ctx, "monthly", lat, lon, start, end, params, community)
}

func (c *Client) fetch(ctx context.Context, temporal string, lat, lon float64, start, end, params, community string) ([]Observation, error) {
	u := BaseURL + "/api/temporal/" + temporal + "/point?" + url.Values{
		"start":      {start},
		"end":        {end},
		"latitude":   {strconv.FormatFloat(lat, 'f', -1, 64)},
		"longitude":  {strconv.FormatFloat(lon, 'f', -1, 64)},
		"community":  {community},
		"parameters": {params},
		"format":     {"JSON"},
	}.Encode()

	body, err := c.Get(ctx, u)
	if err != nil {
		return nil, err
	}

	var resp powerResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	obsLon := resp.Geometry.Coordinates[0]
	obsLat := resp.Geometry.Coordinates[1]

	var out []Observation
	for param, dates := range resp.Properties.Parameter {
		// Sort dates for deterministic output.
		dateKeys := make([]string, 0, len(dates))
		for d := range dates {
			dateKeys = append(dateKeys, d)
		}
		sort.Strings(dateKeys)

		for _, d := range dateKeys {
			out = append(out, Observation{
				Lat:       obsLat,
				Lon:       obsLon,
				Date:      d,
				Parameter: param,
				Value:     dates[d],
			})
		}
	}

	// Sort all observations by date then parameter for stable output.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].Parameter < out[j].Parameter
	})

	return out, nil
}

// Get fetches url and returns the response body. It paces and retries according
// to the client's settings. The caller owns nothing extra; the body is read
// fully and closed here.
func (c *Client) Get(ctx context.Context, url string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		body, retry, err := c.do(ctx, url)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry {
			return nil, err
		}
	}
	return nil, fmt.Errorf("get %s: %w", url, lastErr)
}

func (c *Client) do(ctx context.Context, url string) (body []byte, retry bool, err error) {
	c.pace()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", c.UserAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("http %d", resp.StatusCode)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	return b, false, nil
}

// pace blocks until at least Rate has passed since the previous request.
func (c *Client) pace() {
	if c.Rate <= 0 {
		return
	}
	if wait := c.Rate - time.Since(c.last); wait > 0 {
		time.Sleep(wait)
	}
	c.last = time.Now()
}

func backoff(attempt int) time.Duration {
	d := time.Duration(attempt) * 500 * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}
