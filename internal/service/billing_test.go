package service

import "testing"

func TestFloorCurrencyAmountRetainsSubCentRemainder(t *testing.T) {
	tests := []struct{ input, want string }{
		{input: "0.021", want: "0.02"},
		{input: "0.009", want: "0.00"},
		{input: "12.999", want: "12.99"},
	}
	for _, test := range tests {
		r, err := decimal(test.input)
		if err != nil {
			t.Fatalf("decimal(%q): %v", test.input, err)
		}
		if got := floorCurrencyAmount(r).FloatString(2); got != test.want {
			t.Errorf("floorCurrencyAmount(%s) = %s, want %s", test.input, got, test.want)
		}
	}
}
