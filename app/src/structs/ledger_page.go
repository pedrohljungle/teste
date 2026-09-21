package structs

import "github.com/estrategiahq/pedro-test/app/src/entities"

// LedgerPage is one page of a wallet's ledger, oldest entry first.
//
// The order is the position the database gave each entry when it was written, which never changes
// and never repeats, so a page is stable: entries written while a client is reading are found on a
// later page and none is skipped or seen twice.
type LedgerPage struct {
	Entries []entities.LedgerEntry
	// NextAfter is the position to continue from, and HasMore says whether there is anything to
	// continue to.
	NextAfter int64
	HasMore   bool
}
