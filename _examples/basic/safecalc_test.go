package main

import (
	"fmt"
	"testing"

	"github.com/ams-soft/nforge/helper/safecalc"
)

// TestSafecalc shows the intended use of the money helper: keep amounts as
// integer cents, accept a float only at the edge, and format for display.
func TestSafecalc(t *testing.T) {
	price := safecalc.New(1999, nil) // from the database: 19.99 in cents
	total := safecalc.New(price.Value*3, nil)
	if got := total.Format(); got != "R$ 59,97" {
		t.Errorf("total: %q", got)
	}

	// A float from an external source is rounded to the nearest cent.
	shipping, err := safecalc.NewFromFloat(12.5, nil)
	if err != nil {
		t.Fatal(err)
	}
	grand := safecalc.New(total.Value+shipping.Value, nil)
	if got := grand.Format(); got != "R$ 72,47" {
		t.Errorf("grand total: %q", got)
	}

	usd := safecalc.MoneyOpt{Prefix: "US$", DecimalSep: ".", ThousandsSep: ","}
	if got := safecalc.New(123456789, &usd).Format(); got != "US$ 1,234,567.89" {
		t.Errorf("usd: %q", got)
	}
}

func ExampleMoney_Format() {
	fmt.Println(safecalc.New(123456, nil).Format())
	fmt.Println(safecalc.New(-29, nil).Format())
	// Output:
	// R$ 1.234,56
	// R$ -0,29
}
