package nasapower

import (
	"context"
	"fmt"

	"github.com/tamnd/any-cli/kit"
	"github.com/tamnd/any-cli/kit/errs"
)

// domain.go exposes nasapower as a kit Domain: a driver that a multi-domain
// host (ant) enables with a single blank import,
//
//	import _ "github.com/tamnd/nasapower-cli/nasapower"
//
// exactly as a database/sql program enables a driver with `import _
// "github.com/lib/pq"`. The init below registers it; the host then dereferences
// nasapower:// URIs by routing to the operations Register installs.
func init() { kit.Register(Domain{}) }

// Domain is the NASA POWER driver. It carries no state; the per-run client is
// built by the factory Register hands kit.
type Domain struct{}

// Info describes the scheme, hostnames a pasted link is matched against, and
// the identity used for the binary's help and version.
func (Domain) Info() kit.DomainInfo {
	return kit.DomainInfo{
		Scheme: "nasapower",
		Hosts:  []string{Host},
		Identity: kit.Identity{
			Binary: "nasapower",
			Short:  "A command line for the NASA POWER climate data API.",
			Long: `A command line for the NASA POWER (Prediction Of Worldwide Energy Resources) API.

nasapower reads public climate data from power.larc.nasa.gov, shapes it into
clean records, and prints output that pipes into the rest of your tools. No API
key, nothing to run alongside it.

Available parameter codes include:
  T2M            Air temperature at 2m (°C)
  PRECTOTCORR    Precipitation corrected (mm/day)
  ALLSKY_SFC_SW_DWN  All sky solar radiation (kWh/m²/day)
  WS2M           Wind speed at 2m (m/s)
  RH2M           Relative humidity at 2m (%)

Communities: RE (Renewable Energy), SB (Sustainable Buildings), AG (Agroclimatology)`,
			Site: "https://power.larc.nasa.gov",
			Repo: "https://github.com/tamnd/nasapower-cli",
		},
	}
}

// Register installs the client factory and every operation onto app.
func (Domain) Register(app *kit.App) {
	app.SetClient(newClient)

	kit.Handle(app, kit.OpMeta{
		Name:    "daily",
		Group:   "read",
		List:    true,
		Summary: "Fetch daily climate observations for a location",
		URIType: "point",
	}, getDaily)

	kit.Handle(app, kit.OpMeta{
		Name:    "monthly",
		Group:   "read",
		List:    true,
		Summary: "Fetch monthly climate observations for a location",
		URIType: "point",
	}, getMonthly)
}

// newClient builds the client from the host-resolved config.
func newClient(_ context.Context, cfg kit.Config) (any, error) {
	c := NewClient()
	if cfg.UserAgent != "" {
		c.UserAgent = cfg.UserAgent
	}
	if cfg.Rate > 0 {
		c.Rate = cfg.Rate
	}
	if cfg.Retries > 0 {
		c.Retries = cfg.Retries
	}
	if cfg.Timeout > 0 {
		c.HTTP.Timeout = cfg.Timeout
	}
	return c, nil
}

// --- inputs ---

type dailyInput struct {
	Lat        float64 `kit:"flag" help:"latitude (-90 to 90)" default:"40.71"`
	Lon        float64 `kit:"flag" help:"longitude (-180 to 180)" default:"-74.01"`
	Start      string  `kit:"flag" help:"start date YYYYMMDD" default:"20230101"`
	End        string  `kit:"flag" help:"end date YYYYMMDD" default:"20231231"`
	Parameters string  `kit:"flag" help:"comma-separated parameter codes" default:"T2M,PRECTOTCORR,WS2M"`
	Community  string  `kit:"flag" help:"community: RE|SB|AG" default:"SB"`
	Client     *Client `kit:"inject"`
}

type monthlyInput struct {
	Lat        float64 `kit:"flag" help:"latitude (-90 to 90)" default:"40.71"`
	Lon        float64 `kit:"flag" help:"longitude (-180 to 180)" default:"-74.01"`
	Start      string  `kit:"flag" help:"start year YYYY" default:"2020"`
	End        string  `kit:"flag" help:"end year YYYY" default:"2023"`
	Parameters string  `kit:"flag" help:"comma-separated parameter codes" default:"ALLSKY_SFC_SW_DWN,T2M,WS2M"`
	Community  string  `kit:"flag" help:"community: RE|SB|AG" default:"RE"`
	Client     *Client `kit:"inject"`
}

// --- handlers ---

func getDaily(ctx context.Context, in dailyInput, emit func(*Observation) error) error {
	obs, err := in.Client.Daily(ctx, in.Lat, in.Lon, in.Start, in.End, in.Parameters, in.Community)
	if err != nil {
		return mapErr(err)
	}
	for i := range obs {
		if err := emit(&obs[i]); err != nil {
			return err
		}
	}
	return nil
}

func getMonthly(ctx context.Context, in monthlyInput, emit func(*Observation) error) error {
	obs, err := in.Client.Monthly(ctx, in.Lat, in.Lon, in.Start, in.End, in.Parameters, in.Community)
	if err != nil {
		return mapErr(err)
	}
	for i := range obs {
		if err := emit(&obs[i]); err != nil {
			return err
		}
	}
	return nil
}

// --- Resolver: the URI-native string functions, pure and network-free ---

// Classify turns any accepted input into the canonical (type, id).
// A coordinate pair "lat,lon" classifies as a "point"; a date string
// (YYYYMMDD or YYYYMM) classifies as a "date".
func (Domain) Classify(input string) (uriType, id string, err error) {
	// Try coordinate pair "lat,lon"
	var lat, lon float64
	if n, _ := fmt.Sscanf(input, "%f,%f", &lat, &lon); n == 2 {
		return "point", input, nil
	}
	// Try date string: YYYYMMDD (8 digits) or YYYYMM (6 digits) or YYYY (4 digits)
	if isDateLike(input) {
		return "date", input, nil
	}
	return "", "", errs.Usage("unrecognized nasapower reference: %q (want lat,lon or YYYYMMDD date)", input)
}

// Locate returns the live URL for a (type, id).
func (Domain) Locate(uriType, id string) (string, error) {
	switch uriType {
	case "point":
		return "https://power.larc.nasa.gov/data-access-viewer/", nil
	case "date":
		return "https://power.larc.nasa.gov/data-access-viewer/", nil
	default:
		return "", errs.Usage("nasapower has no resource type %q", uriType)
	}
}

// --- helpers ---

// isDateLike returns true if the string looks like a YYYY, YYYYMM, or YYYYMMDD date.
func isDateLike(s string) bool {
	if len(s) != 4 && len(s) != 6 && len(s) != 8 {
		return false
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

// mapErr converts a library error into the kit error kind that carries the right
// exit code.
func mapErr(err error) error {
	return err
}
