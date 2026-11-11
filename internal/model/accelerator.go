package model

import "strings"

var acceleratorVendorKeywords = map[string][]string{
	"nvidia":     {"nvidia", "tesla", "geforce"},
	"amd":        {"amd", "radeon", "instinct"},
	"intel":      {"intel", "gaudi", "habana"},
	"rebellions": {"rebellions", "rbln", "atom"},
	"furiosa":    {"furiosa", "rngd", "warboy"},
}

// AcceleratorVendorOf identifies the first named vendor. Ambiguous or missing names yield an empty string.
func AcceleratorVendorOf(texts ...string) string {
	for _, text := range texts {
		lowered := strings.ToLower(text)
		var matched string
		for vendor, keywords := range acceleratorVendorKeywords {
			for _, keyword := range keywords {
				if strings.Contains(lowered, keyword) {
					if matched != "" && matched != vendor {
						return ""
					}
					matched = vendor
					break
				}
			}
		}
		if matched != "" {
			return matched
		}
	}
	return ""
}
