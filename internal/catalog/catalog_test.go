package catalog

import (
	"testing"

	"github.com/heismyke/aebello/svc/internal/esimaccess"
)

func TestCharm(t *testing.T) {
	for in, want := range map[int64]int64{390: 399, 399: 399, 400: 449, 449: 449, 450: 499, 570: 599, 1: 49} {
		if got := charm(in); got != want {
			t.Errorf("charm(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestRetail(t *testing.T) {
	p := Pricing{Markup: 2, MinProfit: 1, Overrides: map[string]float64{"AF-29": 1.5, "NG_5_30": 3}}
	cases := []struct {
		slug, location string
		cost           int64 // USD x 10,000
		want           int64 // cents
	}{
		{"KE_1_7", "KE", 19500, 399},       // $1.95 x 2 = $3.90 -> $3.99
		{"ZA_1_7", "ZA", 6800, 199},        // $0.68 x 2 = $1.36, but min profit $1 -> $1.68 -> $1.99
		{"AF-29_1_7", "AF-29", 57000, 899}, // region override 1.5: $8.55 -> $8.99
		{"NG_5_30", "NG", 126800, 3849},    // slug override 3: $38.04 -> $38.49
	}
	for _, c := range cases {
		if got := p.Retail(c.slug, c.location, c.cost); got != c.want {
			t.Errorf("Retail(%s) = %d, want %d", c.slug, got, c.want)
		}
	}
}

func TestBuild(t *testing.T) {
	ng := esimaccess.LocationNetwork{Name: "Nigeria", Code: "NG", Operators: []esimaccess.Operator{{Name: "Glo", NetworkType: "4G"}}}
	ngRegional := esimaccess.LocationNetwork{Name: "Nigeria", Code: "NG", Operators: []esimaccess.Operator{{Name: "MTN", NetworkType: "5G"}}}
	ke := esimaccess.LocationNetwork{Name: "Kenya", Code: "KE", Operators: []esimaccess.Operator{{Name: "Safaricom", NetworkType: "4G"}}}
	pkgs := []esimaccess.Package{
		{Slug: "NG_5_30", Name: "Nigeria 5GB 30Days", Price: 126800, Volume: 5 << 30, DataType: 1, Duration: 30, DurationUnit: "DAY", LocationCode: "NG", Networks: []esimaccess.LocationNetwork{ng}},
		{Slug: "NG_1_7", Name: "Nigeria 1GB 7Days", Price: 28500, Volume: 1 << 30, DataType: 1, Duration: 7, DurationUnit: "DAY", LocationCode: "NG", Networks: []esimaccess.LocationNetwork{ng}},
		{Slug: "AF-29_1_7", Name: "Africa 1GB 7Days", Price: 57000, Volume: 1 << 30, DataType: 1, Duration: 7, DurationUnit: "DAY", LocationCode: "AF-29", Networks: []esimaccess.LocationNetwork{ngRegional, ke}},
		{Slug: "NG_1_Daily", Name: "Nigeria 1GB/Day", Price: 25400, Volume: 1 << 30, DataType: 2, Duration: 1, DurationUnit: "DAY", LocationCode: "NG", Networks: []esimaccess.LocationNetwork{ng}},
		{Slug: "TG_1_7", Name: "Togo 1GB 7Days", Price: 177800, Volume: 1 << 30, DataType: 1, Duration: 7, DurationUnit: "DAY", LocationCode: "TG"},
	}
	c := &Catalog{pricing: Pricing{Markup: 2, MinProfit: 1, Hidden: []string{"TG"}}}
	s := c.build(pkgs)

	if _, ok := s.plans["NG_1_Daily"]; ok {
		t.Error("day pass plans should be skipped")
	}
	if _, ok := s.plans["TG_1_7"]; ok {
		t.Error("hidden locations should be skipped")
	}
	got := s.byCountry["NG"]
	if len(got) != 3 || got[0].Slug != "NG_1_7" || got[1].Slug != "NG_5_30" || got[2].Slug != "AF-29_1_7" {
		t.Fatalf("Nigeria plans in wrong order: %v", slugs(got))
	}
	africa := s.plans["AF-29_1_7"]
	if africa.Kind != "regional" || africa.Region != "Africa" || africa.CountryCount != 2 {
		t.Errorf("regional plan = %+v", africa)
	}
	if n := africa.For("ng").Networks; len(n) != 1 || n[0].Name != "MTN" {
		t.Errorf("regional networks in Nigeria = %v, want MTN", n)
	}
	if len(s.destinations) != 2 || s.destinations[0].Name != "Kenya" {
		t.Fatalf("destinations = %+v", s.destinations)
	}
	nigeria := s.destinations[1]
	if nigeria.FromPrice != 599 || len(nigeria.Networks) != 2 {
		t.Errorf("Nigeria = %+v, want from $5.99 on Glo and MTN", nigeria)
	}
	if s.plans["NG_5_30"].Data != "5 GB" {
		t.Errorf("data label = %q", s.plans["NG_5_30"].Data)
	}
}

func slugs(plans []*Plan) []string {
	var out []string
	for _, p := range plans {
		out = append(out, p.Slug)
	}
	return out
}
