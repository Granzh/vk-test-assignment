package admin

import (
	"os"
	"testing"
)

func TestExamplePromoCSVIsValid(t *testing.T) {
	f, err := os.Open("../../docs/examples/promo_codes.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p, err := ParsePromoCSV(f)
	if err != nil || len(p.Codes) != 5 || len(p.Invalid) != 0 || len(p.Duplicates) != 0 || !p.HeaderSkipped {
		t.Fatalf("%+v, %v", p, err)
	}
}
