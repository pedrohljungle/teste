package structs

import "github.com/estrategiahq/pedro-test/app/src/entities"

// MoneyDTO is the wire form of an amount, {"amount":"25.00","currency":"BRL"}. It is not the
// domain value, which is entities.Money: this is what a handler declares in its requests and
// responses, both because the documentation describes it and because the amount is a string in
// either direction, so nothing on the way in or out is a number that could become a float.
type MoneyDTO struct {
	Amount   string `json:"amount" example:"25.00"`
	Currency string `json:"currency" example:"BRL"`
}

// MoneyDTOOf is the wire form of a domain value.
func MoneyDTOOf(m entities.Money) MoneyDTO {
	return MoneyDTO{Amount: m.Amount(), Currency: string(m.Currency())}
}

// Parse validates the wire form as external input and returns the domain value: at most two
// decimal places, no sign, no exponent, a real currency. It is the same rule the domain applies
// everywhere an amount comes from outside.
func (m MoneyDTO) Parse() (entities.Money, error) {
	return entities.ParseMoney(m.Amount, m.Currency)
}
