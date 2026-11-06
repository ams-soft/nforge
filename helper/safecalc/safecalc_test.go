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

// Round must never turn a finite number into NaN or Inf: too many
// places, or a value too large to scale, leave it as it is.
func TestRoundNeverProducesNonFiniteValues(t *testing.T) {
	cases := []struct {
		val    float64
		places int
		want   float64
	}{
		{1.5, 400, 1.5},
		{1.5, 1000, 1.5},
		{1e300, 10, 1e300},
		{-1e300, 10, -1e300},
		{math.MaxFloat64, 2, math.MaxFloat64},
		{1.005, 2, 1}, // stored as 1.00499999999999989...: float imprecision shows
		{0.125, 2, 0.12},
		{1234, -2, 1200}, // negative places still round to tens, hundreds...
		{2.5, 0, 2},
	}
	for _, c := range cases {
		got := Round(c.val, c.places)
		if got != c.want || math.IsNaN(got) || math.IsInf(got, 0) {
			t.Errorf("Round(%v, %d) = %v, want %v", c.val, c.places, got, c.want)
		}
	}
	if got := Round(math.NaN(), 2); !math.IsNaN(got) {
		t.Errorf("Round(NaN) = %v", got)
	}
	if got := Round(math.Inf(1), 2); !math.IsInf(got, 1) {
		t.Errorf("Round(+Inf) = %v", got)
	}
}

// Currencies with 0 or 3 minor digits.
func TestFormatDecimals(t *testing.T) {
	jpy := MoneyOpt{Prefix: "¥", DecimalSep: ".", ThousandsSep: ",", Decimals: Places(0)}
	kwd := MoneyOpt{Prefix: "KD", DecimalSep: ".", ThousandsSep: ",", Decimals: Places(3)}
	cases := []struct {
		name  string
		cents int64
		opt   MoneyOpt
		want  string
	}{
		{"jpy", 1234567, jpy, "¥ 1,234,567"},
		{"jpy zero", 0, jpy, "¥ 0"},
		{"jpy negative", -5, jpy, "¥ -5"},
		{"kwd", 1234567, kwd, "KD 1,234.567"},
		{"kwd small", 7, kwd, "KD 0.007"},
		{"kwd negative", -1005, kwd, "KD -1.005"},
		{"kwd min int64", math.MinInt64, kwd, "KD -9,223,372,036,854,775.808"},
		{"clamped low", 12, MoneyOpt{DecimalSep: ".", Decimals: Places(-4)}, "12"},
		{"clamped high", 5, MoneyOpt{DecimalSep: ".", Decimals: Places(99)}, "0.000000000000000005"},
	}
	for _, c := range cases {
		if got := New(c.cents, &c.opt).Format(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNewFromFloatDecimals(t *testing.T) {
	m, err := NewFromFloat(1234, &MoneyOpt{Decimals: Places(0)})
	if err != nil || m.Value != 1234 {
		t.Fatalf("0 decimals: %v %v", m, err)
	}
	m, err = NewFromFloat(1.2345, &MoneyOpt{Decimals: Places(3)})
	if err != nil || m.Value != 1234 { // half to even on 1234.5
		t.Fatalf("3 decimals: %v %v", m, err)
	}
	if _, err = NewFromFloat(1e15, &MoneyOpt{Decimals: Places(3)}); err != nil {
		t.Fatalf("in range: %v", err)
	}
	if _, err = NewFromFloat(1e15, &MoneyOpt{Decimals: Places(18)}); err == nil {
		t.Fatal("1e15 with 18 decimals overflows int64")
	}
}

func TestNewDoesNotShareDecimals(t *testing.T) {
	n := 3
	m := New(1, &MoneyOpt{Decimals: &n})
	n = 0
	if *m.Opt.Decimals != 3 {
		t.Fatal("New must copy Decimals")
	}
}
