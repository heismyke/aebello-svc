package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"slices"
)

// Pricing turns wholesale costs into the prices customers see. It is read
// from pricing.json so prices change without a code change.
type Pricing struct {
	// Markup multiplies the wholesale cost, e.g. 2.0 sells at double.
	Markup float64 `json:"markup"`
	// MinProfit is the least we earn per plan, in USD, so cheap plans still
	// cover payment fees.
	MinProfit float64 `json:"minProfit"`
	// Overrides replace Markup for one plan slug ("NG_1_7") or a whole
	// location ("NG", or a region such as "AF-29").
	Overrides map[string]float64 `json:"overrides"`
	// Hidden plan slugs or location codes are not sold at all.
	Hidden []string `json:"hidden"`
}

var defaultPricing = Pricing{Markup: 2, MinProfit: 1}

// LoadPricing reads the pricing file, or returns the defaults when the file
// does not exist.
func LoadPricing(path string) (Pricing, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return defaultPricing, nil
	}
	if err != nil {
		return Pricing{}, err
	}
	p := defaultPricing
	if err := json.Unmarshal(data, &p); err != nil {
		return Pricing{}, fmt.Errorf("%s: %w", path, err)
	}
	if p.Markup < 1 {
		return Pricing{}, fmt.Errorf("%s: markup must be at least 1", path)
	}
	return p, nil
}

func (p Pricing) hidden(slug, location string) bool {
	return slices.Contains(p.Hidden, slug) || slices.Contains(p.Hidden, location)
}

// Retail returns the customer price in cents for a wholesale cost in
// eSIM Access units (USD x 10,000).
func (p Pricing) Retail(slug, location string, costUnits int64) int64 {
	markup := p.Markup
	if m, ok := p.Overrides[location]; ok {
		markup = m
	}
	if m, ok := p.Overrides[slug]; ok {
		markup = m
	}
	cost := float64(costUnits) / 10000
	price := max(cost*markup, cost+p.MinProfit)
	// Round to micro-dollars first so float noise (5.7000000001) cannot add a cent.
	return charm(int64(math.Ceil(math.Round(price*1e6) / 1e4)))
}

// charm rounds cents up to the nearest price ending in .49 or .99 that is not
// below it, e.g. 390 -> 399, 399 -> 399, 400 -> 449, 570 -> 599.
func charm(cents int64) int64 {
	return (cents+50)/50*50 - 1
}
