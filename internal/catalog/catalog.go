// Package catalog keeps the eSIM Access plan list in memory, prices it, and
// answers "which plans work in this country?". The list is small (a few
// thousand plans) and changes rarely, so it is refreshed on a timer instead of
// being stored in the database.
package catalog

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/heismyke/aebello/svc/internal/esimaccess"
)

type Network struct {
	Name string `json:"name"`
	Type string `json:"type"` // 4G, 5G...
}

// Plan is a plan as the apps show it.
type Plan struct {
	Slug         string    `json:"slug"`
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`             // country, regional or global
	Region       string    `json:"region,omitempty"` // e.g. "Africa", for regional and global plans
	CountryCount int       `json:"countryCount"`
	DataBytes    int64     `json:"dataBytes"`
	Data         string    `json:"data"` // "1 GB", "500 MB"
	Days         int       `json:"days"`
	Price        int64     `json:"price"` // cents
	Currency     string    `json:"currency"`
	Speed        string    `json:"speed"`
	TopUp        bool      `json:"topUp"`
	Networks     []Network `json:"networks,omitempty"` // in the country asked about

	CostUnits int64                `json:"-"` // wholesale, USD x 10,000
	countries []string             // ISO codes covered
	networks  map[string][]Network // by ISO code
}

// For returns a copy of the plan with Networks set for one country.
func (p *Plan) For(country string) Plan {
	out := *p
	out.Networks = p.networks[strings.ToUpper(country)]
	return out
}

// Covers reports whether the plan works in a country.
func (p *Plan) Covers(country string) bool {
	return slices.Contains(p.countries, strings.ToUpper(country))
}

type Destination struct {
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Flag      string    `json:"flag"`
	FromPrice int64     `json:"fromPrice"` // cheapest plan, cents
	Currency  string    `json:"currency"`
	PlanCount int       `json:"planCount"`
	Networks  []Network `json:"networks"`
}

type snapshot struct {
	plans        map[string]*Plan   // by slug
	byCountry    map[string][]*Plan // sorted for display
	destinations []Destination      // sorted by name
	loadedAt     time.Time
}

type Catalog struct {
	client  *esimaccess.Client
	pricing Pricing
	db      *pgxpool.Pool // keeps the last list for fast restarts
	current atomic.Pointer[snapshot]
}

func New(client *esimaccess.Client, pricing Pricing, db *pgxpool.Pool) *Catalog {
	return &Catalog{client: client, pricing: pricing, db: db}
}

// Run serves the cached list at once, then refreshes it now and every
// interval until ctx ends. A failed refresh keeps serving the previous list
// and retries a minute later.
func (c *Catalog) Run(ctx context.Context, interval time.Duration) {
	c.loadCache(ctx)
	for {
		wait := interval
		if err := c.Refresh(ctx); err != nil {
			slog.Error("catalog refresh failed", "err", err)
			wait = time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (c *Catalog) Refresh(ctx context.Context) error {
	pkgs, err := c.client.Packages(ctx)
	if err != nil {
		return err
	}
	snap := c.build(pkgs)
	c.current.Store(snap)
	slog.Info("catalog loaded", "plans", len(snap.plans), "destinations", len(snap.destinations))

	data, err := json.Marshal(pkgs)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(ctx, `
		INSERT INTO catalog_cache (id, packages, fetched_at) VALUES (1, $1, now())
		ON CONFLICT (id) DO UPDATE SET packages = excluded.packages, fetched_at = excluded.fetched_at`, data)
	return err
}

func (c *Catalog) loadCache(ctx context.Context) {
	var data []byte
	var fetched time.Time
	err := c.db.QueryRow(ctx, `SELECT packages, fetched_at FROM catalog_cache WHERE id = 1`).Scan(&data, &fetched)
	if err != nil {
		return // first start, or no cache yet
	}
	var pkgs []esimaccess.Package
	if err := json.Unmarshal(data, &pkgs); err != nil {
		slog.Warn("ignoring unreadable catalog cache", "err", err)
		return
	}
	c.current.Store(c.build(pkgs))
	slog.Info("catalog loaded from cache", "fetchedAt", fetched)
}

// Ready reports whether a plan list has been loaded.
func (c *Catalog) Ready() bool { return c.current.Load() != nil }

func (c *Catalog) Destinations() []Destination {
	if s := c.current.Load(); s != nil {
		return s.destinations
	}
	return nil
}

// Destination returns a country and the plans that work there.
func (c *Catalog) Destination(code string) (Destination, []Plan, bool) {
	s := c.current.Load()
	if s == nil {
		return Destination{}, nil, false
	}
	code = strings.ToUpper(code)
	i := slices.IndexFunc(s.destinations, func(d Destination) bool { return d.Code == code })
	if i < 0 {
		return Destination{}, nil, false
	}
	plans := make([]Plan, 0, len(s.byCountry[code]))
	for _, p := range s.byCountry[code] {
		plans = append(plans, p.For(code))
	}
	return s.destinations[i], plans, true
}

// Plan looks a plan up by slug.
func (c *Catalog) Plan(slug string) (*Plan, bool) {
	s := c.current.Load()
	if s == nil {
		return nil, false
	}
	p, ok := s.plans[slug]
	return p, ok
}

// "Africa (21 areas) 1GB 7Days" -> "Africa"
var regionSuffix = regexp.MustCompile(`\s*(\(.*?\))?\s+[\d.]+\s*(MB|GB).*$`)

func (c *Catalog) build(pkgs []esimaccess.Package) *snapshot {
	s := &snapshot{plans: map[string]*Plan{}, byCountry: map[string][]*Plan{}, loadedAt: time.Now()}
	names := map[string]string{}

	for _, pkg := range pkgs {
		// Only fixed data plans for now; day passes are priced per day.
		if pkg.DataType != 1 || pkg.DurationUnit != "DAY" || c.pricing.hidden(pkg.Slug, pkg.LocationCode) {
			continue
		}
		p := &Plan{
			Slug:      pkg.Slug,
			Name:      pkg.Name,
			DataBytes: pkg.Volume,
			Data:      formatBytes(pkg.Volume),
			Days:      pkg.Duration,
			Price:     c.pricing.Retail(pkg.Slug, pkg.LocationCode, pkg.Price),
			Currency:  "USD",
			Speed:     pkg.Speed,
			TopUp:     pkg.TopUpType > 1,
			CostUnits: pkg.Price,
			networks:  map[string][]Network{},
		}
		for _, loc := range pkg.Networks {
			code := strings.ToUpper(loc.Code)
			p.countries = append(p.countries, code)
			names[code] = loc.Name
			for _, op := range loc.Operators {
				p.networks[code] = append(p.networks[code], Network{Name: op.Name, Type: op.NetworkType})
			}
		}
		p.CountryCount = len(p.countries)
		switch {
		case len(pkg.LocationCode) == 2:
			p.Kind = "country"
		case strings.HasPrefix(pkg.LocationCode, "GL"):
			p.Kind, p.Region = "global", "Global"
		default:
			p.Kind, p.Region = "regional", strings.TrimSpace(regionSuffix.ReplaceAllString(pkg.Name, ""))
		}
		s.plans[p.Slug] = p
		for _, code := range p.countries {
			s.byCountry[code] = append(s.byCountry[code], p)
		}
	}

	kindOrder := map[string]int{"country": 0, "regional": 1, "global": 2}
	for code, plans := range s.byCountry {
		slices.SortFunc(plans, func(a, b *Plan) int {
			return cmp.Or(
				cmp.Compare(kindOrder[a.Kind], kindOrder[b.Kind]),
				cmp.Compare(a.Days, b.Days),
				cmp.Compare(a.DataBytes, b.DataBytes),
				cmp.Compare(a.Price, b.Price),
			)
		})
		d := Destination{
			Code: code, Name: names[code], Currency: "USD", PlanCount: len(plans),
			Flag: "https://flagcdn.com/w80/" + strings.ToLower(code) + ".png",
		}
		for _, p := range plans {
			d.Networks = mergeNetworks(d.Networks, p.networks[code])
		}
		d.FromPrice = fromPrice(plans)
		s.destinations = append(s.destinations, d)
	}
	slices.SortFunc(s.destinations, func(a, b Destination) int { return strings.Compare(a.Name, b.Name) })
	return s
}

// fromPrice is the cheapest plan of at least 1 GB, so a 100 MB trial plan
// doesn't make a destination look cheaper than it really is.
func fromPrice(plans []*Plan) int64 {
	var cheapest, cheapestGB int64
	for _, p := range plans {
		if cheapest == 0 || p.Price < cheapest {
			cheapest = p.Price
		}
		if p.DataBytes >= 1<<30 && (cheapestGB == 0 || p.Price < cheapestGB) {
			cheapestGB = p.Price
		}
	}
	return cmp.Or(cheapestGB, cheapest)
}

func mergeNetworks(have, add []Network) []Network {
	for _, n := range add {
		if !slices.ContainsFunc(have, func(h Network) bool { return strings.EqualFold(h.Name, n.Name) }) {
			have = append(have, n)
		}
	}
	return have
}

func formatBytes(b int64) string {
	const gb = 1 << 30
	if b >= gb {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(b)/gb), ".0") + " GB"
	}
	return fmt.Sprintf("%d MB", b>>20)
}
