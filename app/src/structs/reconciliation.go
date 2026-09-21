package structs

import "github.com/estrategiahq/pedro-test/app/src/entities"

// Reconciliation is the result of rebuilding a wallet balance from its ledger and comparing it with
// the balance the wallet stores. Both were read from the same consistent view of the data.
type Reconciliation struct {
	WalletID          string
	StoredBalance     entities.Money
	CalculatedBalance entities.Money
	// Difference is the stored balance minus the calculated one: zero when they agree, positive when
	// the wallet holds more than its ledger accounts for.
	Difference entities.Money
	Consistent bool
	// CheckedEntries is how many ledger entries the balance was rebuilt from, the opening included.
	CheckedEntries int
}
