// Package safecalc holds monetary amounts as an integer number of cents and
// formats them for display. Amounts never pass through float64 once built,
// so formatting is exact over the whole int64 range; floats are accepted only
// at the edge (NewFromFloat) and rounded half-to-even to the nearest cent.
//
// It is a standalone helper: no configuration, no Deps wiring, stdlib only.
package safecalc

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// ErrInvalidAmount reports a float that is NaN, infinite or, once converted
// to cents, outside the int64 range.
var ErrInvalidAmount = errors.New("safecalc: invalid amount")

// MoneyOpt controls how Format renders an amount. Prefix is followed by one
// space when non-empty; Suffix is appended as is (include its own space).
type MoneyOpt struct {
	Prefix       string // currency symbol before the amount, e.g. "R$"
	Suffix       string // text after the amount, e.g. " BRL"
	DecimalSep   string // decimal separator, e.g. ","
	ThousandsSep string // thousands grouping separator, e.g. "."; "" disables grouping

	// Decimals is the number of minor-unit digits of the currency, which Value
	// counts: nil means 2 (BRL, USD, EUR); use Places(0) for JPY and Places(3)
	// for KWD or BHD. Values outside 0..MaxDecimals are clamped by Format.
	Decimals *int
}

// MaxDecimals is the largest supported number of minor-unit digits: 10^18 is
// the biggest power of ten that fits in a uint64.
const MaxDecimals = 18

// Places returns n for MoneyOpt.Decimals, so a literal can say Decimals: safecalc.Places(0).
func Places(n int) *int { return &n }

// decimals is the effective, clamped number of minor-unit digits.
func (o MoneyOpt) decimals() int {
	if o.Decimals == nil {
		return 2
	}
	return min(max(*o.Decimals, 0), MaxDecimals)
}

// BRL returns the Brazilian real format ("R$ 1.234,56"). It is a function,
// not a variable, so no caller can change the default for everyone else.
func BRL() MoneyOpt {
	return MoneyOpt{Prefix: "R$", DecimalSep: ",", ThousandsSep: "."}
}

// Money is an amount in cents plus its display options.
type Money struct {
	Opt   MoneyOpt
	Value int64 // minor units: 123456 is 1234.56 (or 123456 for Places(0), 123.456 for Places(3))
}

// New builds an amount from cents, the exact and preferred constructor. A nil
// opts selects BRL; opts is copied.
func New(cents int64, opts *MoneyOpt) Money {
	o := BRL()
	if opts != nil {
		o = *opts
		if o.Decimals != nil {
			o.Decimals = Places(*o.Decimals) // not shared with the caller's variable
		}
	}
	return Money{Opt: o, Value: cents}
}

// twoTo63 is 2^63 as a float64, the first value above the int64 range.
const twoTo63 = float64(1 << 63)

// pow10 holds 10^0 .. 10^MaxDecimals.
var pow10 = func() (p [MaxDecimals + 1]uint64) {
	p[0] = 1
	for i := 1; i <= MaxDecimals; i++ {
		p[i] = p[i-1] * 10
	}
	return
}()

// NewFromFloat converts value (in currency units, e.g. 19.99) to minor units
// (cents, or 10^-Decimals of the unit), rounding half to even. The float itself may already be inexact (1.005 is
// stored as 1.00499…, so it becomes 100 cents): prefer New with integer cents
// from the source (database, parsed decimal string) whenever possible.
func NewFromFloat(value float64, opts *MoneyOpt) (Money, error) {
	d := MoneyOpt{}
	if opts != nil {
		d = *opts
	}
	cents := math.RoundToEven(value * float64(pow10[d.decimals()]))
	if math.IsNaN(cents) || cents < -twoTo63 || cents >= twoTo63 {
		return Money{}, ErrInvalidAmount
	}
	return New(int64(cents), opts), nil
}

// Round rounds val to the given number of decimal places, half to even. It
// works on float64 and inherits its imprecision (1.005 is stored as 1.00499…
// and rounds to 1); it is a display helper, not a way to compute money. A
// value or a number of places that leaves no digits to round (Round(1e300, 10),
// Round(1.5, 400)) is returned unchanged, never NaN or Inf.
func Round(val float64, places int) float64 {
	shift := math.Pow(10, float64(places))
	scaled := val * shift
	if shift == 0 || math.IsInf(shift, 0) || math.IsInf(scaled, 0) || math.IsNaN(scaled) {
		// Too many places (or too large a value) to round at all: a float64
		// has no digits left there, so the value is already as rounded as it
		// can be. Never turn a finite input into NaN or Inf.
		return val
	}
	return math.RoundToEven(scaled) / shift
}

// Format renders the amount as Prefix, a space, an optional "-", the grouped
// integer part, DecimalSep, Decimals digits (two by default; none, and no
// DecimalSep, for Places(0)) and Suffix: New(-123456, nil).Format() is
// "R$ -1.234,56".
func (m Money) Format() string {
	// The magnitude as uint64 is exact even for math.MinInt64.
	// #nosec G115 -- modulo conversion and negation preserve the magnitude, including MinInt64.
	abs := uint64(m.Value)
	if m.Value < 0 {
		abs = -abs
	}
	places := m.Opt.decimals()
	digits := strconv.FormatUint(abs/pow10[places], 10)

	var b strings.Builder
	if m.Opt.Prefix != "" {
		b.WriteString(m.Opt.Prefix)
		b.WriteByte(' ')
	}
	if m.Value < 0 {
		b.WriteByte('-')
	}
	for i := range len(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteString(m.Opt.ThousandsSep)
		}
		b.WriteByte(digits[i])
	}
	if places > 0 {
		b.WriteString(m.Opt.DecimalSep)
		frac := abs % pow10[places]
		for i := places - 1; i >= 0; i-- { // zero-padded to the full width
			b.WriteByte(byte('0' + frac/pow10[i]%10))
		}
	}
	b.WriteString(m.Opt.Suffix)
	return b.String()
}
