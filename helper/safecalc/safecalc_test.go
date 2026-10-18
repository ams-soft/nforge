package safecalc

import (
	"errors"
	"math"
	"testing"
)

func TestFormat(t *testing.T) {
	cases := map[int64]string{
		0:             "R$ 0,00",
		1:             "R$ 0,01",
		29:            "R$ 0,29", // regression: the float round trip printed 0,28
		57:            "R$ 0,57",
		100:           "R$ 1,00",
		99999:         "R$ 999,99",
		100000:        "R$ 1.000,00",
		123456:        "R$ 1.234,56",
		-5:            "R$ -0,05",
		-29:           "R$ -0,29",
		-123456:       "R$ -1.234,56",
		1<<53 + 1:     "R$ 90.071.992.547.409,93", // exact above 2^53
		math.MaxInt64: "R$ 92.233.720.368.547.758,07",
		math.MinInt64: "R$ -92.233.720.368.547.758,08", // regression: MaxInt64 printed as negative
	}
	for cents, want := range cases {
		if got := New(cents, nil).Format(); got != want {
			t.Errorf("New(%d).Format() = %q, expected %q", cents, got, want)
		}
	}
}

func TestFormatOptions(t *testing.T) {
	usd := MoneyOpt{Prefix: "$", DecimalSep: ".", ThousandsSep: ","}
	eur := MoneyOpt{Suffix: " €", DecimalSep: ",", ThousandsSep: "."}
	plain := MoneyOpt{DecimalSep: "."}
	cases := []struct {
		opt  MoneyOpt
		want string
	}{
		{usd, "$ 1,234,567.89"},
		{eur, "1.234.567,89 €"}, // regression: an empty prefix left a leading space
		{plain, "1234567.89"},
	}
	for _, tc := range cases {
		if got := New(123456789, &tc.opt).Format(); got != tc.want {
			t.Errorf("%+v: %q, expected %q", tc.opt, got, tc.want)
		}
	}
}

func TestNewCopiesOptions(t *testing.T) {
	opt := BRL()
	m := New(100, &opt)
	opt.Prefix = "X"
	if m.Opt.Prefix != "R$" || BRL().Prefix != "R$" {
		t.Fatalf("options were shared: %+v", m.Opt)
	}
}

func TestNewFromFloat(t *testing.T) {
	cases := map[float64]int64{
		0:      0,
		0.29:   29,   // regression: truncation gave 28
		19.99:  1999, // regression: truncation gave 1998
		-0.29:  -29,
		0.125:  12, // half to even
		0.135:  14,
		1234.5: 123450,
		-0.005: 0, // -0.5 cent rounds to even (zero)
	}
	for in, want := range cases {
		m, err := NewFromFloat(in, nil)
		if err != nil || m.Value != want {
			t.Errorf("NewFromFloat(%v) = %d, %v; expected %d", in, m.Value, err, want)
		}
	}
	for _, in := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 1e17, -1e17} {
		if _, err := NewFromFloat(in, nil); !errors.Is(err, ErrInvalidAmount) {
			t.Errorf("NewFromFloat(%v): %v, expected ErrInvalidAmount", in, err)
		}
	}
	// The int64 edges: -2^63 cents is valid, 2^63 cents is not.
	if m, err := NewFromFloat(-math.Exp2(63)/100, nil); err != nil || m.Value != math.MinInt64 {
		t.Errorf("min edge: %d, %v", m.Value, err)
	}
	if _, err := NewFromFloat(math.Exp2(63)/100, nil); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("max edge: %v", err)
	}
}

func TestRound(t *testing.T) {
	cases := []struct {
		in     float64
		places int
		want   float64
	}{
		{0.125, 2, 0.12},
		{0.135, 2, 0.14},
		{2.5, 0, 2},
		{3.5, 0, 4},
		{-2.5, 0, -2},
		{1234.5678, 1, 1234.6},
	}
	for _, tc := range cases {
		if got := Round(tc.in, tc.places); got != tc.want {
			t.Errorf("Round(%v, %d) = %v, expected %v", tc.in, tc.places, got, tc.want)
		}
	}
}
