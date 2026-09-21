package wagering

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	inboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/inbox"
	persistenceiface "github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	walletiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wallet"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

// memory is a storage double that behaves like the real one where the service depends on it: the
// transaction identities are unique, a unit of work keeps every write together or throws every
// one of them away, and a wallet that is not there is reported as such.
type memory struct {
	wallets      map[uuid.UUID]entities.WalletSnapshot
	transactions map[uuid.UUID]entities.WagerTransactionSnapshot
	entries      []entities.LedgerEntrySnapshot
	events       []entities.OutboxEventSnapshot
	inbox        map[string]entities.InboxMessageSnapshot

	atomicCalls int
	// beforeInsert runs as a transaction is about to be inserted, to play the other copy of the
	// operation that wins the race.
	beforeInsert func()
	// failWalletLock makes the wallet read fail, as a database that cannot be reached does.
	failWalletLock error
	// committedElsewhere are transactions another writer committed. A rollback of the unit of work
	// under test must not take them away: they were never part of it.
	committedElsewhere []entities.WagerTransactionSnapshot
}

// commitElsewhere stores a transaction as if another instance had committed it.
func (m *memory) commitElsewhere(snapshot entities.WagerTransactionSnapshot) {
	m.transactions[snapshot.ID] = snapshot
	m.committedElsewhere = append(m.committedElsewhere, snapshot)
}

func newMemory() *memory {
	return &memory{
		wallets:      map[uuid.UUID]entities.WalletSnapshot{},
		transactions: map[uuid.UUID]entities.WagerTransactionSnapshot{},
		inbox:        map[string]entities.InboxMessageSnapshot{},
	}
}

func (m *memory) state() memory {
	saved := memory{
		wallets:      map[uuid.UUID]entities.WalletSnapshot{},
		transactions: map[uuid.UUID]entities.WagerTransactionSnapshot{},
		entries:      append([]entities.LedgerEntrySnapshot(nil), m.entries...),
		events:       append([]entities.OutboxEventSnapshot(nil), m.events...),
		inbox:        map[string]entities.InboxMessageSnapshot{},
	}
	for key, message := range m.inbox {
		saved.inbox[key] = message
	}
	for id, w := range m.wallets {
		saved.wallets[id] = w
	}
	for id, t := range m.transactions {
		saved.transactions[id] = t
	}
	return saved
}

func (m *memory) restore(saved memory) {
	m.wallets, m.transactions, m.entries, m.events, m.inbox = saved.wallets, saved.transactions, saved.entries, saved.events, saved.inbox
	for _, other := range m.committedElsewhere {
		m.transactions[other.ID] = other
	}
}

type unitOfWork struct{ m *memory }

func (u unitOfWork) Atomic(ctx context.Context, fn func(ctx context.Context) error) error {
	u.m.atomicCalls++
	saved := u.m.state()
	if err := fn(ctx); err != nil {
		u.m.restore(saved)
		return err
	}
	return nil
}

type walletStore struct{ m *memory }

func (s walletStore) Insert(context.Context, *entities.Wallet) error { return nil }
func (s walletStore) Get(context.Context, uuid.UUID) (*entities.Wallet, error) {
	return nil, errors.New("not used by this service")
}
func (s walletStore) GetForUpdate(_ context.Context, id uuid.UUID) (*entities.Wallet, error) {
	if s.m.failWalletLock != nil {
		return nil, s.m.failWalletLock
	}
	stored, ok := s.m.wallets[id]
	if !ok {
		return nil, walletiface.ErrNotFound
	}
	return entities.RehydrateWallet(stored)
}
func (s walletStore) Update(_ context.Context, w *entities.Wallet) error {
	s.m.wallets[w.ID()] = w.Snapshot()
	return nil
}
func (s walletStore) InsertEntry(_ context.Context, e entities.LedgerEntry) error {
	s.m.entries = append(s.m.entries, e.Snapshot())
	return nil
}

type wageringStore struct{ m *memory }

func (s wageringStore) Insert(_ context.Context, t *entities.WagerTransaction) error {
	if s.m.beforeInsert != nil {
		hook := s.m.beforeInsert
		s.m.beforeInsert = nil
		hook()
	}
	for _, stored := range s.m.transactions {
		if stored.ProviderID != nil && *stored.ProviderID == t.ProviderID() &&
			(*stored.ExternalTransactionID == t.ExternalTransactionID() || *stored.IdempotencyKey == t.IdempotencyKey()) {
			return wageringiface.ErrDuplicate
		}
	}
	s.m.transactions[t.ID()] = t.Snapshot()
	return nil
}
func (s wageringStore) Update(context.Context, *entities.WagerTransaction) error { return nil }
func (s wageringStore) Get(context.Context, uuid.UUID) (*entities.WagerTransaction, error) {
	return nil, wageringiface.ErrNotFound
}
func (s wageringStore) FindByExternal(_ context.Context, providerID, external string) (*entities.WagerTransaction, error) {
	for _, stored := range s.m.transactions {
		if stored.ProviderID != nil && *stored.ProviderID == providerID && *stored.ExternalTransactionID == external {
			return entities.RehydrateWagerTransaction(stored)
		}
	}
	return nil, wageringiface.ErrNotFound
}
func (s wageringStore) FindByKey(_ context.Context, providerID, key string) (*entities.WagerTransaction, error) {
	for _, stored := range s.m.transactions {
		if stored.ProviderID != nil && *stored.ProviderID == providerID && *stored.IdempotencyKey == key {
			return entities.RehydrateWagerTransaction(stored)
		}
	}
	return nil, wageringiface.ErrNotFound
}

type inboxStore struct{ m *memory }

func inboxKey(consumer, id string) string { return consumer + "|" + id }

func (s inboxStore) Insert(_ context.Context, message *entities.InboxMessage) (bool, error) {
	key := inboxKey(message.ConsumerName(), message.MessageID())
	if _, exists := s.m.inbox[key]; exists {
		return false, nil
	}
	s.m.inbox[key] = message.Snapshot()
	return true, nil
}

func (s inboxStore) Complete(_ context.Context, message *entities.InboxMessage) error {
	s.m.inbox[inboxKey(message.ConsumerName(), message.MessageID())] = message.Snapshot()
	return nil
}

func (s inboxStore) Find(_ context.Context, consumer, id string) (*entities.InboxMessage, error) {
	stored, ok := s.m.inbox[inboxKey(consumer, id)]
	if !ok {
		return nil, inboxiface.ErrNotFound
	}
	return entities.RehydrateInboxMessage(stored)
}

type outboxStore struct{ m *memory }

func (s outboxStore) Insert(_ context.Context, e *entities.OutboxEvent) error {
	s.m.events = append(s.m.events, e.Snapshot())
	return nil
}

func (s outboxStore) Claim(context.Context, string, int, time.Duration, time.Time) ([]*entities.OutboxEvent, error) {
	return nil, errors.New("not used by this service")
}
func (s outboxStore) Complete(context.Context, *entities.OutboxEvent) error { return nil }
func (s outboxStore) Release(context.Context, *entities.OutboxEvent, string) error {
	return nil
}

var fixedNow = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

func newTestService(m *memory) *service {
	next := 0
	return &service{
		uow:      unitOfWork{m},
		wallets:  walletStore{m},
		wagering: wageringStore{m},
		outbox:   outboxStore{m},
		inbox:    inboxStore{m},
		obs:      observability.NewNop(),
		now:      func() time.Time { return fixedNow },
		newID: func() uuid.UUID {
			next++
			return uuid.MustParse(fmt.Sprintf("00000000-0000-7000-8000-%012d", next))
		},
	}
}

func money(t *testing.T, amount string) entities.Money {
	t.Helper()
	m, err := entities.ParseMoney(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// fixture is a wallet stored with a balance, and the operation that targets it.
type fixture struct {
	m      *memory
	svc    *service
	wallet uuid.UUID
	player uuid.UUID
}

func newFixture(t *testing.T, balance string) *fixture {
	t.Helper()
	m := newMemory()
	opening, err := entities.OpenWallet(
		entities.OpeningIDs{
			Wallet:      uuid.MustParse("00000000-0000-7000-8000-00000000f001"),
			Transaction: uuid.MustParse("00000000-0000-7000-8000-00000000f003"),
			Entry:       uuid.MustParse("00000000-0000-7000-8000-00000000f004"),
		},
		uuid.MustParse("00000000-0000-7000-8000-00000000f002"), money(t, balance), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	m.wallets[opening.Wallet.ID()] = opening.Wallet.Snapshot()
	return &fixture{m: m, svc: newTestService(m), wallet: opening.Wallet.ID(), player: opening.Wallet.PlayerID()}
}

func (f *fixture) op(t *testing.T, mutate func(*entities.ExternalOperation)) entities.ExternalOperation {
	t.Helper()
	op := entities.ExternalOperation{
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		PlayerID:              f.player.String(),
		WalletID:              f.wallet.String(),
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 money(t, "25.00"),
	}
	if mutate != nil {
		mutate(&op)
	}
	return op
}

func (f *fixture) balance(t *testing.T) (string, int64) {
	t.Helper()
	w, err := entities.RehydrateWallet(f.m.wallets[f.wallet])
	if err != nil {
		t.Fatal(err)
	}
	return w.Balance().Amount(), w.Version()
}

func eventTypes(events []entities.OutboxEventSnapshot) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.EventType
	}
	return types
}

func TestABetIsProcessedDebitsTheWalletAndProducesTheLedgerAndTheEvents(t *testing.T) {
	f := newFixture(t, "1000.00")
	ctx := observability.WithCorrelationID(context.Background(), "req-1")

	out, err := f.svc.Submit(ctx, f.op(t, nil))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	tx := out.Transaction
	if out.Replay || tx.Status() != entities.StatusProcessed {
		t.Fatalf("replay %v, status %s", out.Replay, tx.Status())
	}
	if tx.ResultBalance().Amount() != "975.00" || tx.ResultWalletVersion() != 2 {
		t.Fatalf("result = %s at version %d", tx.ResultBalance(), tx.ResultWalletVersion())
	}
	if balance, version := f.balance(t); balance != "975.00" || version != 2 {
		t.Fatalf("wallet = %s at version %d", balance, version)
	}
	if len(f.m.entries) != 1 || f.m.entries[0].Direction != "DEBIT" ||
		f.m.entries[0].BalanceBeforeMinor != 100000 || f.m.entries[0].BalanceAfterMinor != 97500 {
		t.Fatalf("entries = %+v", f.m.entries)
	}
	if got := eventTypes(f.m.events); len(got) != 2 || got[0] != "WagerTransactionProcessed" || got[1] != "WalletBalanceChanged" {
		t.Fatalf("events = %v", got)
	}
	for _, event := range f.m.events {
		if event.CorrelationID != "req-1" {
			t.Errorf("%s carries correlation %q", event.EventType, event.CorrelationID)
		}
	}
	if f.m.atomicCalls != 1 {
		t.Fatalf("the writes ran in %d units of work, want one", f.m.atomicCalls)
	}
}

func TestAWinCreditsTheWallet(t *testing.T) {
	f := newFixture(t, "100.00")

	out, err := f.svc.Submit(context.Background(), f.op(t, func(op *entities.ExternalOperation) {
		op.Kind, op.Money = "WIN", money(t, "40.00")
	}))

	if err != nil || out.Transaction.Status() != entities.StatusProcessed {
		t.Fatalf("outcome %+v, error %v", out, err)
	}
	if balance, version := f.balance(t); balance != "140.00" || version != 2 {
		t.Fatalf("wallet = %s at version %d", balance, version)
	}
	if len(f.m.entries) != 1 || f.m.entries[0].Direction != "CREDIT" {
		t.Fatalf("entries = %+v", f.m.entries)
	}
}

func TestALossIsProcessedWithoutTouchingTheBalanceTheVersionOrTheLedger(t *testing.T) {
	f := newFixture(t, "100.00")

	out, err := f.svc.Submit(context.Background(), f.op(t, func(op *entities.ExternalOperation) {
		op.Kind, op.Money = "LOSS", money(t, "0.00")
	}))

	if err != nil || out.Transaction.Status() != entities.StatusProcessed {
		t.Fatalf("outcome %+v, error %v", out, err)
	}
	if balance, version := f.balance(t); balance != "100.00" || version != 1 {
		t.Fatalf("a loss changed the wallet: %s at version %d", balance, version)
	}
	if len(f.m.entries) != 0 {
		t.Fatalf("a loss produced %d ledger entries", len(f.m.entries))
	}
	if got := eventTypes(f.m.events); len(got) != 1 || got[0] != "WagerTransactionProcessed" {
		t.Fatalf("events = %v, want only WagerTransactionProcessed", got)
	}
	if out.Transaction.ResultBalance().Amount() != "100.00" || out.Transaction.ResultWalletVersion() != 1 {
		t.Fatalf("result = %s at version %d", out.Transaction.ResultBalance(), out.Transaction.ResultWalletVersion())
	}
}

func TestABetWithoutFundsIsStoredAsRejectedAndMovesNothing(t *testing.T) {
	f := newFixture(t, "10.00")

	out, err := f.svc.Submit(context.Background(), f.op(t, nil))

	if err != nil {
		t.Fatalf("a business rejection is an outcome, not an error: %v", err)
	}
	if out.Transaction.Status() != entities.StatusRejected || out.Transaction.FailureCode() != entities.FailureInsufficientFunds {
		t.Fatalf("status %s, code %s", out.Transaction.Status(), out.Transaction.FailureCode())
	}
	if balance, version := f.balance(t); balance != "10.00" || version != 1 {
		t.Fatalf("a rejection changed the wallet: %s at version %d", balance, version)
	}
	if len(f.m.entries) != 0 {
		t.Fatal("a rejection produced a ledger entry")
	}
	if got := eventTypes(f.m.events); len(got) != 1 || got[0] != "WagerTransactionRejected" {
		t.Fatalf("events = %v", got)
	}
	if len(f.m.transactions) != 1 {
		t.Fatalf("the rejection was not stored: %d transactions", len(f.m.transactions))
	}
}

func TestAnOperationThatBreaksTheValuePolicyIsStoredAsRejected(t *testing.T) {
	cases := map[string]func(*entities.ExternalOperation){
		"a bet of zero":     func(op *entities.ExternalOperation) { op.Money = money(t, "0.00") },
		"a win of zero":     func(op *entities.ExternalOperation) { op.Kind, op.Money = "WIN", money(t, "0.00") },
		"a loss above zero": func(op *entities.ExternalOperation) { op.Kind, op.Money = "LOSS", money(t, "0.01") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "100.00")

			out, err := f.svc.Submit(context.Background(), f.op(t, mutate))

			if err != nil {
				t.Fatalf("Submit: %v", err)
			}
			if out.Transaction.Status() != entities.StatusRejected || out.Transaction.FailureCode() != entities.FailureInvalidAmount {
				t.Fatalf("status %s, code %s", out.Transaction.Status(), out.Transaction.FailureCode())
			}
			if balance, version := f.balance(t); balance != "100.00" || version != 1 {
				t.Fatalf("the wallet changed: %s at version %d", balance, version)
			}
		})
	}
}

func TestAnOperationInAnotherCurrencyThanTheWalletIsRejected(t *testing.T) {
	f := newFixture(t, "100.00")
	usd, err := entities.ParseMoney("25.00", "USD")
	if err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"BET", "WIN", "LOSS"} {
		out, err := f.svc.Submit(context.Background(), f.op(t, func(op *entities.ExternalOperation) {
			op.Kind, op.Money = kind, usd
			op.ExternalTransactionID, op.IdempotencyKey = "ext-"+kind, "key-"+kind
			if kind == "LOSS" {
				op.Money, _ = entities.ZeroMoney("USD")
			}
		}))
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if out.Transaction.Status() != entities.StatusRejected || out.Transaction.FailureCode() != entities.FailureCurrencyMismatch {
			t.Errorf("%s: status %s, code %s", kind, out.Transaction.Status(), out.Transaction.FailureCode())
		}
	}
	if balance, version := f.balance(t); balance != "100.00" || version != 1 {
		t.Fatalf("the wallet changed: %s at version %d", balance, version)
	}
}

func TestAnOperationForAnotherPlayerThanTheWalletOwnerIsRejected(t *testing.T) {
	f := newFixture(t, "100.00")

	out, err := f.svc.Submit(context.Background(), f.op(t, func(op *entities.ExternalOperation) {
		op.PlayerID = uuid.NewString()
	}))

	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Transaction.FailureCode() != entities.FailurePlayerMismatch {
		t.Fatalf("code = %s, want %s", out.Transaction.FailureCode(), entities.FailurePlayerMismatch)
	}
	if balance, _ := f.balance(t); balance != "100.00" {
		t.Fatalf("the wallet changed: %s", balance)
	}
}

func TestAnOperationForAWalletThatDoesNotExistIsRejectedAndNotStored(t *testing.T) {
	f := newFixture(t, "100.00")

	_, err := f.svc.Submit(context.Background(), f.op(t, func(op *entities.ExternalOperation) {
		op.WalletID = uuid.NewString()
	}))

	code, ok := entities.RejectionCode(err)
	if !ok || code != entities.FailureWalletNotFound {
		t.Fatalf("error = %v, want a rejection with %s", err, entities.FailureWalletNotFound)
	}
	if len(f.m.transactions) != 0 || len(f.m.events) != 0 || len(f.m.entries) != 0 {
		t.Fatal("something was stored for a wallet that does not exist")
	}
}

func TestARepeatedOperationReturnsTheStoredOutcomeAndAppliesNothingAgain(t *testing.T) {
	f := newFixture(t, "1000.00")
	first, err := f.svc.Submit(context.Background(), f.op(t, nil))
	if err != nil {
		t.Fatal(err)
	}

	second, err := f.svc.Submit(context.Background(), f.op(t, nil))

	if err != nil {
		t.Fatalf("the replay: %v", err)
	}
	if !second.Replay || second.Transaction.ID() != first.Transaction.ID() {
		t.Fatalf("replay %v, id %s, want the first id %s", second.Replay, second.Transaction.ID(), first.Transaction.ID())
	}
	if len(f.m.entries) != 1 || len(f.m.events) != 2 || len(f.m.transactions) != 1 {
		t.Fatalf("a replay wrote again: %d entries, %d events, %d transactions", len(f.m.entries), len(f.m.events), len(f.m.transactions))
	}
	if balance, version := f.balance(t); balance != "975.00" || version != 2 {
		t.Fatalf("wallet = %s at version %d, the bet must have been applied once", balance, version)
	}
}

func TestAReplayReturnsTheBalanceOfTheOriginalProcessingNotTheCurrentOne(t *testing.T) {
	f := newFixture(t, "1000.00")
	if _, err := f.svc.Submit(context.Background(), f.op(t, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Submit(context.Background(), f.op(t, func(op *entities.ExternalOperation) {
		op.Kind, op.Money = "WIN", money(t, "225.00")
		op.ExternalTransactionID, op.IdempotencyKey = "win-1", "key-win-1"
	})); err != nil {
		t.Fatal(err)
	}
	if balance, _ := f.balance(t); balance != "1200.00" {
		t.Fatalf("setup: the wallet holds %s", balance)
	}

	replay, err := f.svc.Submit(context.Background(), f.op(t, nil))

	if err != nil || !replay.Replay {
		t.Fatalf("replay %v, error %v", replay.Replay, err)
	}
	if replay.Transaction.ResultBalance().Amount() != "975.00" {
		t.Fatalf("the replay reports %s, want the 975.00 of the original processing", replay.Transaction.ResultBalance())
	}
}

func TestReplayingARejectionReturnsTheSameRejection(t *testing.T) {
	f := newFixture(t, "10.00")
	first, err := f.svc.Submit(context.Background(), f.op(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	// The balance changes afterwards, so a re-evaluation would now succeed.
	stored := f.m.wallets[f.wallet]
	stored.BalanceMinor = 100000
	f.m.wallets[f.wallet] = stored

	replay, err := f.svc.Submit(context.Background(), f.op(t, nil))

	if err != nil || !replay.Replay {
		t.Fatalf("replay %v, error %v", replay.Replay, err)
	}
	if replay.Transaction.ID() != first.Transaction.ID() || replay.Transaction.Status() != entities.StatusRejected {
		t.Fatalf("status %s, id %s: a rejection must be replayed, not re-evaluated", replay.Transaction.Status(), replay.Transaction.ID())
	}
}

func TestTheSameKeyWithDifferentContentIsAConflictAndAppliesNothing(t *testing.T) {
	f := newFixture(t, "1000.00")
	if _, err := f.svc.Submit(context.Background(), f.op(t, nil)); err != nil {
		t.Fatal(err)
	}

	_, err := f.svc.Submit(context.Background(), f.op(t, func(op *entities.ExternalOperation) {
		op.Money = money(t, "30.00")
	}))

	if !errors.Is(err, wageringiface.ErrIdempotencyConflict) {
		t.Fatalf("error = %v, want ErrIdempotencyConflict", err)
	}
	if balance, version := f.balance(t); balance != "975.00" || version != 2 {
		t.Fatalf("the conflicting operation changed the wallet: %s at version %d", balance, version)
	}
	if len(f.m.transactions) != 1 {
		t.Fatalf("%d transactions stored, want only the first", len(f.m.transactions))
	}
}

func TestTheSameProviderTransactionUnderAnotherKeyIsAConflict(t *testing.T) {
	f := newFixture(t, "1000.00")
	if _, err := f.svc.Submit(context.Background(), f.op(t, nil)); err != nil {
		t.Fatal(err)
	}

	_, err := f.svc.Submit(context.Background(), f.op(t, func(op *entities.ExternalOperation) {
		op.IdempotencyKey = "another-key"
	}))

	if !errors.Is(err, wageringiface.ErrIdempotencyConflict) {
		t.Fatalf("error = %v, want ErrIdempotencyConflict", err)
	}
	if balance, _ := f.balance(t); balance != "975.00" {
		t.Fatalf("the operation was applied a second time: %s", balance)
	}
}

func TestTheSameKeyOfAnotherProviderIsAnotherOperation(t *testing.T) {
	f := newFixture(t, "1000.00")
	if _, err := f.svc.Submit(context.Background(), f.op(t, nil)); err != nil {
		t.Fatal(err)
	}

	out, err := f.svc.Submit(context.Background(), f.op(t, func(op *entities.ExternalOperation) {
		op.ProviderID = "provider-b"
	}))

	if err != nil || out.Replay {
		t.Fatalf("replay %v, error %v: keys are scoped by provider", out.Replay, err)
	}
	if balance, _ := f.balance(t); balance != "950.00" {
		t.Fatalf("wallet = %s, both providers' bets must apply", balance)
	}
}

func TestACopyThatLosesTheRaceForTheInsertResolvesAsAReplay(t *testing.T) {
	f := newFixture(t, "1000.00")
	// Both copies pass the lookup. Before this one inserts, the other one commits, and the
	// database refuses this insert on the unique index: exactly what has to resolve as a replay.
	var winner *entities.WagerTransaction
	f.m.beforeInsert = func() {
		other := newTestService(f.m)
		other.newID = func() uuid.UUID { return uuid.MustParse("00000000-0000-7000-8000-0000000000aa") }
		candidate, err := entities.NewExternalTransaction(other.newID(), f.op(t, nil), fixedNow)
		if err != nil {
			t.Fatal(err)
		}
		if err := candidate.MarkProcessed(money(t, "975.00"), 2, fixedNow); err != nil {
			t.Fatal(err)
		}
		f.m.commitElsewhere(candidate.Snapshot())
		winner = candidate
	}

	out, err := f.svc.Submit(context.Background(), f.op(t, nil))

	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !out.Replay || out.Transaction.ID() != winner.ID() {
		t.Fatalf("replay %v, id %s, want the winner %s", out.Replay, out.Transaction.ID(), winner.ID())
	}
	if len(f.m.entries) != 0 {
		t.Fatal("the losing copy left a ledger entry behind")
	}
}

func TestACopyThatLosesTheRaceToADifferentContentIsAConflict(t *testing.T) {
	f := newFixture(t, "1000.00")
	f.m.beforeInsert = func() {
		other, err := entities.NewExternalTransaction(uuid.New(), f.op(t, func(op *entities.ExternalOperation) {
			op.Money = money(t, "99.00")
		}), fixedNow)
		if err != nil {
			t.Fatal(err)
		}
		if err := other.MarkProcessed(money(t, "901.00"), 2, fixedNow); err != nil {
			t.Fatal(err)
		}
		f.m.commitElsewhere(other.Snapshot())
	}

	_, err := f.svc.Submit(context.Background(), f.op(t, nil))

	if !errors.Is(err, wageringiface.ErrIdempotencyConflict) {
		t.Fatalf("error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestADuplicateThatCannotBeFoundAgainIsReportedAsWhatItIs(t *testing.T) {
	f := newFixture(t, "1000.00")
	f.svc.wagering = duplicateAlways{wageringStore{f.m}}

	_, err := f.svc.Submit(context.Background(), f.op(t, nil))

	if !errors.Is(err, wageringiface.ErrDuplicate) {
		t.Fatalf("error = %v, want ErrDuplicate", err)
	}
}

// duplicateAlways refuses every insert as a duplicate while finding nothing, which is what a row
// that was deleted between the refusal and the second lookup would look like.
type duplicateAlways struct{ wageringStore }

func (duplicateAlways) Insert(context.Context, *entities.WagerTransaction) error {
	return wageringiface.ErrDuplicate
}

func TestAnOperationThatIsNotWellFormedStoresNothingAndOpensNoUnitOfWork(t *testing.T) {
	cases := map[string]struct {
		mutate func(*entities.ExternalOperation)
		want   error
	}{
		"an unknown kind":             {func(op *entities.ExternalOperation) { op.Kind = "GAMBLE" }, entities.ErrInvalidTransaction},
		"no provider":                 {func(op *entities.ExternalOperation) { op.ProviderID = "" }, entities.ErrInvalidTransaction},
		"a player that is not a UUID": {func(op *entities.ExternalOperation) { op.PlayerID = "p1" }, entities.ErrInvalidTransaction},
		"OPENING over the API":        {func(op *entities.ExternalOperation) { op.Kind = "OPENING" }, entities.ErrOpeningNotAllowed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "100.00")

			_, err := f.svc.Submit(context.Background(), f.op(t, tc.mutate))

			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if f.m.atomicCalls != 0 || len(f.m.transactions) != 0 {
				t.Fatal("something was stored for input that was malformed before it started")
			}
		})
	}
}

func TestReversalsAreNotSupportedYetAndStoreNothing(t *testing.T) {
	for _, kind := range []string{"REFUND", "ROLLBACK"} {
		f := newFixture(t, "100.00")

		_, err := f.svc.Submit(context.Background(), f.op(t, func(op *entities.ExternalOperation) {
			op.Kind, op.ReferenceExternalTransactionID = kind, "bet-1"
		}))

		if !errors.Is(err, wageringiface.ErrKindNotSupported) {
			t.Errorf("%s: error = %v, want ErrKindNotSupported", kind, err)
		}
		if len(f.m.transactions) != 0 {
			t.Errorf("%s stored a transaction", kind)
		}
	}
}

func TestAStorageFailureRollsBackAndIsReportedAsUnavailable(t *testing.T) {
	f := newFixture(t, "100.00")
	f.m.failWalletLock = persistenceiface.ErrUnavailable

	_, err := f.svc.Submit(context.Background(), f.op(t, nil))

	if !errors.Is(err, persistenceiface.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if len(f.m.transactions) != 0 || len(f.m.events) != 0 {
		t.Fatal("something was stored although the unit of work failed")
	}
}

func TestABalanceThatCannotBeRepresentedIsStoredAsFailedForAuditWithoutEvents(t *testing.T) {
	f := newFixture(t, "1.00")
	stored := f.m.wallets[f.wallet]
	stored.BalanceMinor = math.MaxInt64
	f.m.wallets[f.wallet] = stored

	out, err := f.svc.Submit(context.Background(), f.op(t, func(op *entities.ExternalOperation) {
		op.Kind = "WIN"
	}))

	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if out.Transaction.Status() != entities.StatusFailed || out.Transaction.FailureCode() != entities.FailureInternal {
		t.Fatalf("status %s, code %s", out.Transaction.Status(), out.Transaction.FailureCode())
	}
	if len(f.m.events) != 0 || len(f.m.entries) != 0 {
		t.Fatal("a failed operation must not publish events or move the ledger")
	}
}

func TestAnEventGetsACorrelationIdEvenWhenTheCallerSetNone(t *testing.T) {
	f := newFixture(t, "100.00")

	if _, err := f.svc.Submit(context.Background(), f.op(t, nil)); err != nil {
		t.Fatal(err)
	}
	for _, event := range f.m.events {
		if event.CorrelationID == "" {
			t.Errorf("%s was stored without a correlation id", event.EventType)
		}
	}
}

func TestTheServiceBuiltForProductionUsesTheRealClockAndIds(t *testing.T) {
	m := newMemory()
	built, ok := NewService(unitOfWork{m}, walletStore{m}, wageringStore{m}, outboxStore{m}, inboxStore{m}, observability.NewNop()).(*service)
	if !ok {
		t.Fatal("NewService must build the service")
	}
	if id := built.newID(); id == uuid.Nil || id.Version() != 7 {
		t.Fatalf("newID = %s, want a version 7 UUID", id)
	}
	if time.Since(built.now()) > time.Minute {
		t.Fatal("now is not the current time")
	}
}
