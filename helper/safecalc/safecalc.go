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
}

// BRL returns the Brazilian real format ("R$ 1.234,56"). It is a function,
// not a variable, so no caller can change the default for everyone else.
func BRL() MoneyOpt {
	return MoneyOpt{Prefix: "R$", DecimalSep: ",", ThousandsSep: "."}
}

// Money is an amount in cents plus its display options.
type Money struct {
	Opt   MoneyOpt
	Value int64 // cents: 123456 is 1234.56
}

// New builds an amount from cents, the exact and preferred constructor. A nil
// opts selects BRL; opts is copied.
func New(cents int64, opts *MoneyOpt) Money {
	o := BRL()
	if opts != nil {
		o = *opts
	}
	return Money{Opt: o, Value: cents}
}

// twoTo63 is 2^63 as a float64, the first value above the int64 range.
const twoTo63 = float64(1 << 63)

// NewFromFloat converts value (in currency units, e.g. 19.99) to cents,
// rounding half to even. The float itself may already be inexact (1.005 is
// stored as 1.00499…, so it becomes 100 cents): prefer New with integer cents
// from the source (database, parsed decimal string) whenever possible.
func NewFromFloat(value float64, opts *MoneyOpt) (Money, error) {
	cents := math.RoundToEven(value * 100)
	if math.IsNaN(cents) || cents < -twoTo63 || cents >= twoTo63 {
		return Money{}, ErrInvalidAmount
	}
	return New(int64(cents), opts), nil
}

// Round rounds val to the given number of decimal places, half to even. It
// works on float64 and inherits its imprecision (2.675 is stored as 2.67499…
// and rounds to 2.67); it is a display helper, not a way to compute money.
func Round(val float64, places int) float64 {
	shift := math.Pow(10, float64(places))
	return math.RoundToEven(val*shift) / shift
}

// Format renders the amount as Prefix, a space, an optional "-", the grouped
// integer part, DecimalSep, two decimal digits and Suffix:
// New(-123456, nil).Format() is "R$ -1.234,56".
func (m Money) Format() string {
	// The magnitude as uint64 is exact even for math.MinInt64.
	abs := uint64(m.Value)
	if m.Value < 0 {
		abs = -abs
	}
	digits := strconv.FormatUint(abs/100, 10)

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
	b.WriteString(m.Opt.DecimalSep)
	cents := abs % 100
	b.WriteByte(byte('0' + cents/10))
	b.WriteByte(byte('0' + cents%10))
	b.WriteString(m.Opt.Suffix)
	return b.String()
}
