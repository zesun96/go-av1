// Package preset maps SVT-style speed presets to encoder tool settings.
package preset

// Config is the resolved encoder tool configuration.
type Config struct {
	Number         int
	SearchRange    int
	HierarchicalME bool
	IntegerOnly    bool
	Compound       bool
}

// Resolve returns settings for presets 0 (slowest) through 13 (fastest).
func Resolve(number int) Config {
	if number < 0 {
		number = 0
	}
	if number > 13 {
		number = 13
	}
	cfg := Config{
		Number: number, SearchRange: 8,
		HierarchicalME: number >= 9,
		Compound:       number <= 12,
	}
	switch {
	case number <= 4:
		cfg.SearchRange = 8
	case number <= 8:
		cfg.SearchRange = 6
	case number <= 10:
		cfg.SearchRange = 5
	case number <= 12:
		cfg.SearchRange = 4
	default:
		cfg.SearchRange = 2
		cfg.IntegerOnly = true
	}
	return cfg
}
