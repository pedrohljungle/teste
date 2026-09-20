package entities

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Money errors. Each is a sentinel so a caller classifies a failure with errors.Is instead of
// reading a message. Every parsing failure also matches ErrInvalidAmount.
var (
	// ErrInvalidAmount is any amount that is not a plain decimal with at most two places.
	ErrInvalidAmount = errors.New("invalid amount")
	// ErrNegativeAmount is a sign on an external amount, where a negative value is never valid.
	ErrNegativeAmount = errors.New("negative amount")
	// ErrMoneyOverflow is a value that does not fit an int64 count of minor units.
	ErrMoneyOverflow = errors.New("money overflow")
	// ErrInvalidCurrency is a code that is not an ISO 4217 alphabetic code.
	ErrInvalidCurrency = errors.New("invalid currency")
	// ErrCurrencyMismatch is arithmetic or comparison between two different currencies.
	ErrCurrencyMismatch = errors.New("currency mismatch")
	// ErrUninitializedMoney is the zero value of Money, which carries no currency.
	ErrUninitializedMoney = errors.New("uninitialized money")
)

const (
	// moneyScale is the number of decimal places, fixed for every currency.
	moneyScale = 2
	// minorPerUnit is 10^moneyScale: how many minor units make one unit.
	minorPerUnit = 100
)

// iso4217 is the set of active ISO 4217 alphabetic codes that ParseCurrency accepts. Funds
// codes, metals and the test code XTS are left out on purpose: none of them is a currency a
// player holds a balance in.
var iso4217 = map[string]struct{}{
	"AED": {}, "AFN": {}, "ALL": {}, "AMD": {}, "ANG": {}, "AOA": {}, "ARS": {}, "AUD": {},
	"AWG": {}, "AZN": {}, "BAM": {}, "BBD": {}, "BDT": {}, "BGN": {}, "BHD": {}, "BIF": {},
	"BMD": {}, "BND": {}, "BOB": {}, "BRL": {}, "BSD": {}, "BTN": {}, "BWP": {}, "BYN": {},
	"BZD": {}, "CAD": {}, "CDF": {}, "CHF": {}, "CLP": {}, "CNY": {}, "COP": {}, "CRC": {},
	"CUP": {}, "CVE": {}, "CZK": {}, "DJF": {}, "DKK": {}, "DOP": {}, "DZD": {}, "EGP": {},
	"ERN": {}, "ETB": {}, "EUR": {}, "FJD": {}, "FKP": {}, "GBP": {}, "GEL": {}, "GHS": {},
	"GIP": {}, "GMD": {}, "GNF": {}, "GTQ": {}, "GYD": {}, "HKD": {}, "HNL": {}, "HTG": {},
	"HUF": {}, "IDR": {}, "ILS": {}, "INR": {}, "IQD": {}, "IRR": {}, "ISK": {}, "JMD": {},
	"JOD": {}, "JPY": {}, "KES": {}, "KGS": {}, "KHR": {}, "KMF": {}, "KPW": {}, "KRW": {},
	"KWD": {}, "KYD": {}, "KZT": {}, "LAK": {}, "LBP": {}, "LKR": {}, "LRD": {}, "LSL": {},
	"LYD": {}, "MAD": {}, "MDL": {}, "MGA": {}, "MKD": {}, "MMK": {}, "MNT": {}, "MOP": {},
	"MRU": {}, "MUR": {}, "MVR": {}, "MWK": {}, "MXN": {}, "MYR": {}, "MZN": {}, "NAD": {},
	"NGN": {}, "NIO": {}, "NOK": {}, "NPR": {}, "NZD": {}, "OMR": {}, "PAB": {}, "PEN": {},
	"PGK": {}, "PHP": {}, "PKR": {}, "PLN": {}, "PYG": {}, "QAR": {}, "RON": {}, "RSD": {},
	"RUB": {}, "RWF": {}, "SAR": {}, "SBD": {}, "SCR": {}, "SDG": {}, "SEK": {}, "SGD": {},
	"SHP": {}, "SLE": {}, "SOS": {}, "SRD": {}, "SSP": {}, "STN": {}, "SVC": {}, "SYP": {},
	"SZL": {}, "THB": {}, "TJS": {}, "TMT": {}, "TND": {}, "TOP": {}, "TRY": {}, "TTD": {},
	"TWD": {}, "TZS": {}, "UAH": {}, "UGX": {}, "USD": {}, "UYU": {}, "UZS": {}, "VES": {},
	"VND": {}, "VUV": {}, "WST": {}, "XAF": {}, "XCD": {}, "XOF": {}, "XPF": {}, "YER": {},
	"ZAR": {}, "ZMW": {}, "ZWG": {},
}

// Currency is an ISO 4217 alphabetic code, upper case.
type Currency string

// ParseCurrency validates a currency code against the ISO 4217 list. A lower case code is
// normalised to upper case, and that normalisation happens before the idempotency hash.
func ParseCurrency(code string) (Currency, error) {
	upper := strings.ToUpper(code)
	if _, ok := iso4217[upper]; !ok {
		return "", fmt.Errorf("%w: %q", ErrInvalidCurrency, code)
	}
	return Currency(upper), nil
}

// String is the ISO code.
func (c Currency) String() string { return string(c) }

// Money is an immutable value object: an amount in minor units and the currency it is in.
//
// The amount is an int64 count of hundredths, so nothing in parsing, arithmetic, serialisation
// or persistence goes through a float. The limit is 92,233,720,368,547,758.07 in either
// direction, and every operation that could cross it reports ErrMoneyOverflow instead of
// wrapping.
//
// The scale is fixed at two places for every currency. A currency with another exponent (JPY
// has none, KWD has three) would need its own scale and is out of scope.
//
// The zero value carries no currency and is rejected by every operation.
type Money struct {
	minor    int64
	currency Currency
}

// NewMoney builds a value from minor units. It accepts a negative amount, because a difference
// or an internal calculation is legitimately negative; external input goes through ParseMoney.
func NewMoney(minor int64, currency string) (Money, error) {
	c, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	return Money{minor: minor, currency: c}, nil
}

// ZeroMoney is the zero amount in the given currency.
func ZeroMoney(currency string) (Money, error) {
	return NewMoney(0, currency)
}

// ParseMoney reads an amount received from outside the system, such as an HTTP body or a queue
// message. It accepts digits with an optional fraction of at most two places, and rejects
// everything else instead of correcting it: empty, a sign, whitespace, NaN, Infinity,
// scientific notation and a scale above two. Nothing is rounded.
//
// The forms "25", "25.5" and "25.50" are the same amount, and all normalise to 25.50 before
// the idempotency hash is computed.
func ParseMoney(amount, currency string) (Money, error) {
	c, err := ParseCurrency(currency)
	if err != nil {
		return Money{}, err
	}
	minor, err := parseMinor(amount)
	if err != nil {
		return Money{}, err
	}
	return Money{minor: minor, currency: c}, nil
}

// Minor is the amount in minor units, which is what the database stores.
func (m Money) Minor() int64 { return m.minor }

// Currency is the ISO code.
func (m Money) Currency() Currency { return m.currency }

// IsInitialized reports whether the value carries a currency.
func (m Money) IsInitialized() bool { return m.currency != "" }

// IsZero reports an initialised amount of zero.
func (m Money) IsZero() bool { return m.IsInitialized() && m.minor == 0 }

// IsPositive reports an initialised amount above zero.
func (m Money) IsPositive() bool { return m.IsInitialized() && m.minor > 0 }

// IsNegative reports an initialised amount below zero.
func (m Money) IsNegative() bool { return m.IsInitialized() && m.minor < 0 }

// Amount is the decimal string of the contract, always with two places: "25.00", "-5.00".
func (m Money) Amount() string {
	negative := m.minor < 0
	magnitude := uint64(m.minor)
	if negative {
		// Negating in uint64 also works for math.MinInt64, which has no int64 counterpart.
		magnitude = -magnitude
	}
	text := fmt.Sprintf("%d.%02d", magnitude/minorPerUnit, magnitude%minorPerUnit)
	if negative {
		return "-" + text
	}
	return text
}

// String is the amount followed by the currency.
func (m Money) String() string {
	return m.Amount() + " " + string(m.currency)
}

// Add returns m + other.
func (m Money) Add(other Money) (Money, error) {
	if err := m.sameCurrency(other); err != nil {
		return Money{}, err
	}
	if (other.minor > 0 && m.minor > math.MaxInt64-other.minor) ||
		(other.minor < 0 && m.minor < math.MinInt64-other.minor) {
		return Money{}, fmt.Errorf("%w: %s + %s", ErrMoneyOverflow, m, other)
	}
	return Money{minor: m.minor + other.minor, currency: m.currency}, nil
}

// Sub returns m - other.
func (m Money) Sub(other Money) (Money, error) {
	if err := m.sameCurrency(other); err != nil {
		return Money{}, err
	}
	if (other.minor < 0 && m.minor > math.MaxInt64+other.minor) ||
		(other.minor > 0 && m.minor < math.MinInt64+other.minor) {
		return Money{}, fmt.Errorf("%w: %s - %s", ErrMoneyOverflow, m, other)
	}
	return Money{minor: m.minor - other.minor, currency: m.currency}, nil
}

// Neg returns -m.
func (m Money) Neg() (Money, error) {
	if !m.IsInitialized() {
		return Money{}, ErrUninitializedMoney
	}
	if m.minor == math.MinInt64 {
		return Money{}, fmt.Errorf("%w: -%s", ErrMoneyOverflow, m)
	}
	return Money{minor: -m.minor, currency: m.currency}, nil
}

// Compare returns -1, 0 or 1 when m is below, equal to or above other.
func (m Money) Compare(other Money) (int, error) {
	if err := m.sameCurrency(other); err != nil {
		return 0, err
	}
	switch {
	case m.minor < other.minor:
		return -1, nil
	case m.minor > other.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

// Equal reports whether both hold the same amount in the same currency. Different currencies
// are an error and not a quiet false, which would read as "different amounts".
func (m Money) Equal(other Money) (bool, error) {
	cmp, err := m.Compare(other)
	if err != nil {
		return false, err
	}
	return cmp == 0, nil
}

func (m Money) sameCurrency(other Money) error {
	if !m.IsInitialized() || !other.IsInitialized() {
		return ErrUninitializedMoney
	}
	if m.currency != other.currency {
		return fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, m.currency, other.currency)
	}
	return nil
}

// moneyWire is the shape of the contract: {"amount":"25.00","currency":"BRL"}.
type moneyWire struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

// MarshalJSON writes the contract shape. The amount is always a string, never a JSON number.
func (m Money) MarshalJSON() ([]byte, error) {
	if !m.IsInitialized() {
		return nil, ErrUninitializedMoney
	}
	return json.Marshal(moneyWire{Amount: m.Amount(), Currency: string(m.currency)})
}

// UnmarshalJSON reads the contract shape with the rules of ParseMoney, since the only thing
// decoded from JSON is external input. An amount sent as a JSON number is rejected: decoding
// it would be the first step of a float.
func (m *Money) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var wire moneyWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAmount, err)
	}
	parsed, err := ParseMoney(wire.Amount, wire.Currency)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// parseMinor turns "25.50" into 2550 with integer arithmetic only.
func parseMinor(amount string) (int64, error) {
	if amount == "" {
		return 0, fmt.Errorf("%w: empty", ErrInvalidAmount)
	}
	if strings.HasPrefix(amount, "-") {
		return 0, fmt.Errorf("%w: %w: %q", ErrInvalidAmount, ErrNegativeAmount, amount)
	}

	whole, fraction, hasFraction := strings.Cut(amount, ".")
	if !isDigits(whole) || (hasFraction && !isDigits(fraction)) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, amount)
	}
	if len(fraction) > moneyScale {
		return 0, fmt.Errorf("%w: %q has more than %d decimal places", ErrInvalidAmount, amount, moneyScale)
	}

	units, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %w: %q", ErrInvalidAmount, ErrMoneyOverflow, amount)
	}
	// A fraction of one digit is tenths: "25.5" is 25.50.
	hundredths := int64(0)
	if fraction != "" {
		hundredths, err = strconv.ParseInt((fraction + "0")[:moneyScale], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%w: %q", ErrInvalidAmount, amount)
		}
	}
	if units > (math.MaxInt64-hundredths)/minorPerUnit {
		return 0, fmt.Errorf("%w: %w: %q", ErrInvalidAmount, ErrMoneyOverflow, amount)
	}
	return units*minorPerUnit + hundredths, nil
}

// isDigits is true for a non-empty run of ASCII digits. It is written out instead of using
// unicode.IsDigit, which would accept digits from other scripts.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
