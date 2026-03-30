package money

import (
	"fmt"
	"math"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// ToSatang converts baht (float64) to satang (int64).
// 1 baht = 100 satang.
func ToSatang(baht float64) int64 {
	return int64(math.Round(baht * 100))
}

// ToBaht converts satang (int64) to baht (float64).
func ToBaht(satang int64) float64 {
	return float64(satang) / 100
}

// FormatTHB formats satang as Thai Baht string (e.g., "1,234.50 บาท").
func FormatTHB(satang int64) string {
	baht := ToBaht(satang)
	p := message.NewPrinter(language.Thai)
	return p.Sprintf("%.2f บาท", baht)
}

// FormatTHBNumber formats satang as a number string without currency suffix.
func FormatTHBNumber(satang int64) string {
	baht := ToBaht(satang)
	return fmt.Sprintf("%.2f", baht)
}
