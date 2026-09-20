package entities

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func mustMoney(t *testing.T, amount, currency string) Money {
	t.Helper()
	m, err := ParseMoney(amount, currency)
	if err != nil {
		t.Fatalf("ParseMoney(%q, %q): %v", amount, currency, err)
	}
	return m
}

func mustMinor(t *testing.T, minor int64, currency string) Money {
	t.Helper()
	m, err := NewMoney(minor, currency)
	if err != nil {
		t.Fatalf("NewMoney(%d, %q): %v", minor, currency, err)
	}
	return m
}

func TestParseMoneyAcceptsPlainDecimals(t *testing.T) {
	cases := []struct {
		in    string
		minor int64
		out   string
	}{
		{"25.00", 2500, "25.00"},
		{"0.00", 0, "0.00"},
		{"0", 0, "0.00"},
		{"0.01", 1, "0.01"},
		{"0.10", 10, "0.10"},
		{"25", 2500, "25.00"},
		{"25.5", 2550, "25.50"},
		{"25.05", 2505, "25.05"},
		{"007.10", 710, "7.10"},
		{"1000000.99", 100000099, "1000000.99"},
		{"92233720368547758.07", math.MaxInt64, "92233720368547758.07"},
	}
	for _, tc := range cases {
		m, err := ParseMoney(tc.in, "BRL")
		if err != nil {
			t.Errorf("ParseMoney(%q): unexpected error %v", tc.in, err)
			continue
		}
		if m.Minor() != tc.minor {
			t.Errorf("ParseMoney(%q).Minor() = %d, want %d", tc.in, m.Minor(), tc.minor)
		}
		if m.Amount() != tc.out {
			t.Errorf("ParseMoney(%q).Amount() = %q, want %q", tc.in, m.Amount(), tc.out)
		}
	}
}

func TestParseMoneyNormalisesEquivalentForms(t *testing.T) {
	want := mustMoney(t, "25.50", "BRL")
	for _, in := range []string{"25.5", "25.50", "025.50"} {
		got := mustMoney(t, in, "BRL")
		if got != want {
			t.Errorf("ParseMoney(%q) = %v, want %v", in, got, want)
		}
	}
	if mustMoney(t, "25", "BRL") != mustMoney(t, "25.00", "BRL") {
		t.Error("25 and 25.00 must be the same amount")
	}
}

func TestParseMoneyRejectsInvalidAmounts(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", ErrInvalidAmount},
		{"blank", " ", ErrInvalidAmount},
		{"NaN", "NaN", ErrInvalidAmount},
		{"Infinity", "Infinity", ErrInvalidAmount},
		{"negative Infinity", "-Infinity", ErrNegativeAmount},
		{"scientific lower", "1e3", ErrInvalidAmount},
		{"scientific upper", "1E3", ErrInvalidAmount},
		{"scientific with fraction", "2.5e1", ErrInvalidAmount},
		{"two dots", "1.2.3", ErrInvalidAmount},
		{"trailing dot", "25.", ErrInvalidAmount},
		{"leading dot", ".5", ErrInvalidAmount},
		{"plus sign", "+5.00", ErrInvalidAmount},
		{"negative", "-5.00", ErrNegativeAmount},
		{"negative zero", "-0.00", ErrNegativeAmount},
		{"three decimals", "25.001", ErrInvalidAmount},
		{"three decimals of zero", "25.000", ErrInvalidAmount},
		{"comma separator", "25,00", ErrInvalidAmount},
		{"thousands separator", "1,000.00", ErrInvalidAmount},
		{"underscore", "1_000.00", ErrInvalidAmount},
		{"hexadecimal", "0x10", ErrInvalidAmount},
		{"trailing space", "25.00 ", ErrInvalidAmount},
		{"leading space", " 25.00", ErrInvalidAmount},
		{"other script digits", "٢٥.٠٠", ErrInvalidAmount},
		{"letters", "abc", ErrInvalidAmount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseMoney(tc.in, "BRL")
			if !errors.Is(err, tc.want) {
				t.Fatalf("ParseMoney(%q) error = %v, want %v", tc.in, err, tc.want)
			}
			if !errors.Is(err, ErrInvalidAmount) {
				t.Fatalf("ParseMoney(%q) error = %v, must also match ErrInvalidAmount", tc.in, err)
			}
		})
	}
}

func TestParseMoneyDetectsOverflow(t *testing.T) {
	cases := []string{
		"92233720368547758.08",
		"92233720368547759.00",
		"99999999999999999999.99",
		"9223372036854775808",
	}
	for _, in := range cases {
		_, err := ParseMoney(in, "BRL")
		if !errors.Is(err, ErrMoneyOverflow) {
			t.Errorf("ParseMoney(%q) error = %v, want ErrMoneyOverflow", in, err)
		}
	}
}

func TestParseMoneyRejectsInvalidCurrency(t *testing.T) {
	for _, code := range []string{"", "BR", "BRLL", "XXX", "ZZZ", "123", "B R", "€"} {
		_, err := ParseMoney("1.00", code)
		if !errors.Is(err, ErrInvalidCurrency) {
			t.Errorf("ParseMoney with currency %q error = %v, want ErrInvalidCurrency", code, err)
		}
	}
}

func TestCurrencyIsNormalisedToUpperCase(t *testing.T) {
	m := mustMoney(t, "1.00", "brl")
	if m.Currency() != "BRL" {
		t.Fatalf("Currency() = %q, want BRL", m.Currency())
	}
}

func TestZeroMoneyIsAnInitialisedZeroInItsCurrency(t *testing.T) {
	z, err := ZeroMoney("USD")
	if err != nil {
		t.Fatalf("ZeroMoney: %v", err)
	}
	if !z.IsZero() || !z.IsInitialized() || z.Currency() != "USD" || z.Amount() != "0.00" {
		t.Fatalf("ZeroMoney(USD) = %v", z)
	}
	if _, err := ZeroMoney("nope"); !errors.Is(err, ErrInvalidCurrency) {
		t.Fatalf("ZeroMoney with a bad currency: error = %v, want ErrInvalidCurrency", err)
	}
}

func TestUninitialisedMoneyIsRejectedEverywhere(t *testing.T) {
	var zero Money
	valid := mustMoney(t, "1.00", "BRL")

	if zero.IsInitialized() || zero.IsZero() || zero.IsPositive() || zero.IsNegative() {
		t.Error("the zero value must not report any state")
	}
	if _, err := zero.Add(valid); !errors.Is(err, ErrUninitializedMoney) {
		t.Errorf("zero.Add: error = %v, want ErrUninitializedMoney", err)
	}
	if _, err := valid.Add(zero); !errors.Is(err, ErrUninitializedMoney) {
		t.Errorf("valid.Add(zero): error = %v, want ErrUninitializedMoney", err)
	}
	if _, err := valid.Sub(zero); !errors.Is(err, ErrUninitializedMoney) {
		t.Errorf("valid.Sub(zero): error = %v, want ErrUninitializedMoney", err)
	}
	if _, err := zero.Neg(); !errors.Is(err, ErrUninitializedMoney) {
		t.Errorf("zero.Neg: error = %v, want ErrUninitializedMoney", err)
	}
	if _, err := zero.Compare(valid); !errors.Is(err, ErrUninitializedMoney) {
		t.Errorf("zero.Compare: error = %v, want ErrUninitializedMoney", err)
	}
	if _, err := zero.MarshalJSON(); !errors.Is(err, ErrUninitializedMoney) {
		t.Errorf("zero.MarshalJSON: error = %v, want ErrUninitializedMoney", err)
	}
}

func TestNewMoneyAcceptsNegativeAmounts(t *testing.T) {
	m := mustMinor(t, -500, "BRL")
	if !m.IsNegative() || m.Amount() != "-5.00" {
		t.Fatalf("NewMoney(-500) = %v", m)
	}
	if got := mustMinor(t, -1, "BRL").Amount(); got != "-0.01" {
		t.Fatalf("Amount() of -1 minor = %q, want -0.01", got)
	}
	if got := mustMinor(t, math.MinInt64, "BRL").Amount(); got != "-92233720368547758.08" {
		t.Fatalf("Amount() of MinInt64 = %q", got)
	}
}

func TestMoneyArithmetic(t *testing.T) {
	ten := mustMoney(t, "10.00", "BRL")
	three := mustMoney(t, "3.25", "BRL")

	sum, err := ten.Add(three)
	if err != nil || sum.Amount() != "13.25" {
		t.Fatalf("10.00 + 3.25 = %v, %v", sum, err)
	}
	diff, err := ten.Sub(three)
	if err != nil || diff.Amount() != "6.75" {
		t.Fatalf("10.00 - 3.25 = %v, %v", diff, err)
	}
	below, err := three.Sub(ten)
	if err != nil || below.Amount() != "-6.75" || !below.IsNegative() {
		t.Fatalf("3.25 - 10.00 = %v, %v", below, err)
	}
	neg, err := ten.Neg()
	if err != nil || neg.Amount() != "-10.00" {
		t.Fatalf("-(10.00) = %v, %v", neg, err)
	}
	back, err := neg.Neg()
	if err != nil || back != ten {
		t.Fatalf("-(-10.00) = %v, %v", back, err)
	}
}

func TestMoneyOperationsDoNotMutateTheirOperands(t *testing.T) {
	a := mustMoney(t, "10.00", "BRL")
	b := mustMoney(t, "3.00", "BRL")
	_, _ = a.Add(b)
	_, _ = a.Sub(b)
	_, _ = a.Neg()
	if a.Minor() != 1000 || b.Minor() != 300 {
		t.Fatalf("operands changed: a=%v b=%v", a, b)
	}
}

func TestMoneyArithmeticRejectsMismatchedCurrencies(t *testing.T) {
	brl := mustMoney(t, "10.00", "BRL")
	usd := mustMoney(t, "10.00", "USD")

	if _, err := brl.Add(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Add: error = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := brl.Sub(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Sub: error = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := brl.Compare(usd); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Compare: error = %v, want ErrCurrencyMismatch", err)
	}
	// An error and not a quiet false: "different currency" must not read as "different amount".
	if eq, err := brl.Equal(usd); eq || !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Equal = %v, %v, want false and ErrCurrencyMismatch", eq, err)
	}
}

func TestMoneyArithmeticDetectsOverflow(t *testing.T) {
	top := mustMinor(t, math.MaxInt64, "BRL")
	bottom := mustMinor(t, math.MinInt64, "BRL")
	one := mustMinor(t, 1, "BRL")

	if _, err := top.Add(one); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("max + 0.01: error = %v, want ErrMoneyOverflow", err)
	}
	if _, err := bottom.Add(mustMinor(t, -1, "BRL")); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("min + -0.01: error = %v, want ErrMoneyOverflow", err)
	}
	if _, err := bottom.Sub(one); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("min - 0.01: error = %v, want ErrMoneyOverflow", err)
	}
	if _, err := top.Sub(mustMinor(t, -1, "BRL")); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("max - -0.01: error = %v, want ErrMoneyOverflow", err)
	}
	if _, err := bottom.Neg(); !errors.Is(err, ErrMoneyOverflow) {
		t.Errorf("-min: error = %v, want ErrMoneyOverflow", err)
	}

	// The boundary itself is reachable: overflow starts one minor unit past it.
	if got, err := top.Sub(one); err != nil || got.Minor() != math.MaxInt64-1 {
		t.Errorf("max - 0.01 = %v, %v", got, err)
	}
	if got, err := top.Add(mustMinor(t, math.MinInt64, "BRL")); err != nil || got.Minor() != -1 {
		t.Errorf("max + min = %v, %v", got, err)
	}
	if got, err := top.Neg(); err != nil || got.Minor() != -math.MaxInt64 {
		t.Errorf("-max = %v, %v", got, err)
	}
}

func TestMoneyComparison(t *testing.T) {
	small := mustMoney(t, "1.00", "BRL")
	large := mustMoney(t, "2.00", "BRL")

	for _, tc := range []struct {
		a, b Money
		want int
	}{{small, large, -1}, {large, small, 1}, {small, small, 0}} {
		got, err := tc.a.Compare(tc.b)
		if err != nil || got != tc.want {
			t.Errorf("Compare(%v, %v) = %d, %v, want %d", tc.a, tc.b, got, err, tc.want)
		}
	}
	if eq, err := small.Equal(mustMoney(t, "1", "BRL")); err != nil || !eq {
		t.Errorf("1.00 must equal 1: %v, %v", eq, err)
	}
	if eq, err := small.Equal(large); err != nil || eq {
		t.Errorf("1.00 must not equal 2.00: %v, %v", eq, err)
	}
}

func TestMoneyStatePredicates(t *testing.T) {
	if !mustMoney(t, "0", "BRL").IsZero() {
		t.Error("0.00 must be zero")
	}
	if !mustMoney(t, "0.01", "BRL").IsPositive() {
		t.Error("0.01 must be positive")
	}
	if mustMoney(t, "0", "BRL").IsPositive() || mustMoney(t, "0", "BRL").IsNegative() {
		t.Error("zero is neither positive nor negative")
	}
	if got := mustMoney(t, "25", "BRL").String(); got != "25.00 BRL" {
		t.Errorf("String() = %q", got)
	}
}

func TestMoneyJSONUsesTheContractShape(t *testing.T) {
	raw, err := json.Marshal(mustMoney(t, "25", "BRL"))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(raw) != `{"amount":"25.00","currency":"BRL"}` {
		t.Fatalf("Marshal = %s", raw)
	}

	var back Money
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back != mustMoney(t, "25.00", "BRL") {
		t.Fatalf("round trip = %v", back)
	}

	negative, err := json.Marshal(mustMinor(t, -500, "BRL"))
	if err != nil || string(negative) != `{"amount":"-5.00","currency":"BRL"}` {
		t.Fatalf("Marshal of a difference = %s, %v", negative, err)
	}
}

func TestMoneyJSONRejectsWhatParseMoneyRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{"amount as a JSON number", `{"amount":25.00,"currency":"BRL"}`, ErrInvalidAmount},
		{"amount as an integer", `{"amount":25,"currency":"BRL"}`, ErrInvalidAmount},
		{"missing amount", `{"currency":"BRL"}`, ErrInvalidAmount},
		{"empty amount", `{"amount":"","currency":"BRL"}`, ErrInvalidAmount},
		{"negative amount", `{"amount":"-1.00","currency":"BRL"}`, ErrNegativeAmount},
		{"scale above two", `{"amount":"1.001","currency":"BRL"}`, ErrInvalidAmount},
		{"scientific notation", `{"amount":"1e2","currency":"BRL"}`, ErrInvalidAmount},
		{"missing currency", `{"amount":"1.00"}`, ErrInvalidCurrency},
		{"unknown currency", `{"amount":"1.00","currency":"XXX"}`, ErrInvalidCurrency},
		{"not an object", `"25.00"`, ErrInvalidAmount},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var m Money
			err := json.Unmarshal([]byte(tc.body), &m)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Unmarshal(%s) error = %v, want %v", tc.body, err, tc.want)
			}
			if m.IsInitialized() {
				t.Fatalf("a rejected value must stay uninitialised, got %v", m)
			}
		})
	}
}

func TestMoneyJSONNullLeavesTheValueUninitialised(t *testing.T) {
	var wrapper struct {
		Money Money `json:"money"`
	}
	if err := json.Unmarshal([]byte(`{"money":null}`), &wrapper); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if wrapper.Money.IsInitialized() {
		t.Fatal("null must not produce an initialised value")
	}
}
