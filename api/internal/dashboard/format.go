package dashboard

import "strconv"

// formatPercent renders "<prefix> 91.2% <suffix>".
//
// Alert messages are assembled here rather than with fmt so every alert reads
// the same way and a percentage never arrives with fifteen decimal places.
func formatPercent(prefix string, value float64, suffix string) string {
	return prefix + " " + trimFloat(value) + "% " + suffix
}

// formatFloat renders "<prefix> 2.4 <suffix>".
func formatFloat(prefix string, value float64, suffix string) string {
	return prefix + " " + trimFloat(value) + " " + suffix
}

// trimFloat renders a number with at most one decimal place, dropping a
// trailing ".0".
func trimFloat(value float64) string {
	rendered := strconv.FormatFloat(value, 'f', 1, 64)
	if len(rendered) > 2 && rendered[len(rendered)-2:] == ".0" {
		return rendered[:len(rendered)-2]
	}
	return rendered
}
