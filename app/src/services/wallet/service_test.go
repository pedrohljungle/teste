package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	persistenceiface "github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
	walletiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wallet"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

// memory is a storage double with the one behaviour the service depends on being right: a unit of
// work that keeps every write together or throws every one of them away.
type memory struct {
	wallets      map[uuid.UUID]entities.WalletSnapshot
	transactions map[uuid.UUID]entities.WagerTransactionSnapshot
	entries      []entities.LedgerEntrySnapshot
	events       []entities.OutboxEventSnapshot

	atomicCalls int
	// failOutbox makes the outbox refuse an insert, to break a unit of work halfway through.
	failOutbox error
}

func newMemory() *memory {
	return &memory{
		wallets:      map[uuid.UUID]entities.WalletSnapshot{},
		transactions: map[uuid.UUID]entities.WagerTransactionSnapshot{},
	}
}

func (m *memory) copyState() memory {
	saved := memory{
		wallets:      map[uuid.UUID]entities.WalletSnapshot{},
		transactions: map[uuid.UUID]entities.WagerTransactionSnapshot{},
		entries:      append([]entities.LedgerEntrySnapshot(nil), m.entries...),
		events:       append([]entities.OutboxEventSnapshot(nil), m.events...),
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
	m.wallets, m.transactions, m.entries, m.events = saved.wallets, saved.transactions, saved.entries, saved.events
}

type unitOfWork struct{ m *memory }

func (u unitOfWork) Atomic(ctx context.Context, fn func(ctx context.Context) error) error {
	u.m.atomicCalls++
	saved := u.m.copyState()
	if err := fn(ctx); err != nil {
		u.m.restore(saved)
		return err
	}
	return nil
}

type walletStore struct{ m *memory }

func (s walletStore) Insert(_ context.Context, w *entities.Wallet) error {
	for _, stored := range s.m.wallets {
		if stored.PlayerID == w.PlayerID() && stored.Currency == string(w.Currency()) {
			return walletiface.ErrAlreadyExists
		}
	}
	s.m.wallets[w.ID()] = w.Snapshot()
	return nil
}

func (s walletStore) Get(_ context.Context, id uuid.UUID) (*entities.Wallet, error) {
	stored, ok := s.m.wallets[id]
	if !ok {
		return nil, walletiface.ErrNotFound
	}
	return entities.RehydrateWallet(stored)
}

func (s walletStore) GetForUpdate(ctx context.Context, id uuid.UUID) (*entities.Wallet, error) {
	return s.Get(ctx, id)
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
	s.m.transactions[t.ID()] = t.Snapshot()
	return nil
}
func (s wageringStore) Update(context.Context, *entities.WagerTransaction) error { return nil }
func (s wageringStore) Get(context.Context, uuid.UUID) (*entities.WagerTransaction, error) {
	return nil, errors.New("not used by this service")
}
func (s wageringStore) FindByExternal(context.Context, string, string) (*entities.WagerTransaction, error) {
	return nil, errors.New("not used by this service")
}
func (s wageringStore) FindByKey(context.Context, string, string) (*entities.WagerTransaction, error) {
	return nil, errors.New("not used by this service")
}

type outboxStore struct{ m *memory }

func (s outboxStore) Insert(_ context.Context, e *entities.OutboxEvent) error {
	if s.m.failOutbox != nil {
		return s.m.failOutbox
	}
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
		obs:      observability.NewNop(),
		now:      func() time.Time { return fixedNow },
		newID: func() uuid.UUID {
			next++
			return uuid.MustParse(fmt.Sprintf("00000000-0000-7000-8000-%012d", next))
		},
	}
}

func brl(t *testing.T, amount string) entities.Money {
	t.Helper()
	m, err := entities.ParseMoney(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

var player = uuid.MustParse("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1")

func eventTypes(events []entities.OutboxEventSnapshot) []string {
	types := make([]string, len(events))
	for i, e := range events {
		types[i] = e.EventType
	}
	return types
}

func TestOpeningAWalletWithABalanceStoresEverythingInOneUnitOfWork(t *testing.T) {
	m := newMemory()
	ctx := observability.WithCorrelationID(context.Background(), "req-42")

	wallet, err := newTestService(m).Open(ctx, player, brl(t, "1000.00"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if wallet.Balance().Amount() != "1000.00" || wallet.Version() != 1 || wallet.PlayerID() != player {
		t.Fatalf("wallet = %s at version %d for %s", wallet.Balance(), wallet.Version(), wallet.PlayerID())
	}
	if m.atomicCalls != 1 {
		t.Fatalf("the writes ran in %d units of work, want exactly one", m.atomicCalls)
	}
	if len(m.wallets) != 1 || len(m.transactions) != 1 || len(m.entries) != 1 || len(m.events) != 2 {
		t.Fatalf("stored %d wallets, %d transactions, %d entries, %d events; want 1, 1, 1, 2",
			len(m.wallets), len(m.transactions), len(m.entries), len(m.events))
	}

	var opening entities.WagerTransactionSnapshot
	for _, stored := range m.transactions {
		opening = stored
	}
	if opening.Kind != "OPENING" || opening.Status != "PROCESSED" || opening.Origin != "INTERNAL" {
		t.Fatalf("opening = %+v", opening)
	}
	if m.entries[0].Direction != "CREDIT" || m.entries[0].BalanceBeforeMinor != 0 || m.entries[0].BalanceAfterMinor != 100000 {
		t.Fatalf("entry = %+v", m.entries[0])
	}
	if got := eventTypes(m.events); got[0] != "WagerTransactionProcessed" || got[1] != "WalletBalanceChanged" {
		t.Fatalf("events = %v", got)
	}
}

func TestTheEventsOfAnOpeningCarryTheCorrelationAndCausation(t *testing.T) {
	m := newMemory()
	ctx := observability.WithCorrelationID(context.Background(), "req-42")

	if _, err := newTestService(m).Open(ctx, player, brl(t, "10.00")); err != nil {
		t.Fatalf("Open: %v", err)
	}

	var opening entities.WagerTransactionSnapshot
	for _, stored := range m.transactions {
		opening = stored
	}
	for _, event := range m.events {
		if event.CorrelationID != "req-42" {
			t.Errorf("%s carries correlation %q, want req-42", event.EventType, event.CorrelationID)
		}
	}
	changed := m.events[1]
	if changed.CausationID == nil || *changed.CausationID != opening.ID.String() {
		t.Errorf("WalletBalanceChanged causation = %v, want the opening transaction %s", changed.CausationID, opening.ID)
	}

	var envelope struct {
		Data struct {
			WalletVersion int64 `json:"walletVersion"`
		} `json:"data"`
	}
	if err := json.Unmarshal(changed.Payload, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.WalletVersion != 1 {
		t.Errorf("the opening event reports wallet version %d, want 1", envelope.Data.WalletVersion)
	}
}

func TestAnEventGetsACorrelationIdEvenWhenTheCallerSetNone(t *testing.T) {
	m := newMemory()

	if _, err := newTestService(m).Open(context.Background(), player, brl(t, "10.00")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, event := range m.events {
		if event.CorrelationID == "" {
			t.Errorf("%s was stored without a correlation id", event.EventType)
		}
	}
}

func TestOpeningAWalletWithAZeroBalanceStoresOnlyTheWallet(t *testing.T) {
	m := newMemory()

	wallet, err := newTestService(m).Open(context.Background(), player, brl(t, "0.00"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if wallet.Version() != 1 || !wallet.Balance().IsZero() {
		t.Fatalf("wallet = %s at version %d", wallet.Balance(), wallet.Version())
	}
	if len(m.wallets) != 1 || len(m.transactions) != 0 || len(m.entries) != 0 || len(m.events) != 0 {
		t.Fatalf("stored %d wallets, %d transactions, %d entries, %d events; want 1, 0, 0, 0",
			len(m.wallets), len(m.transactions), len(m.entries), len(m.events))
	}
}

func TestASecondWalletForTheSamePlayerAndCurrencyIsRefusedAndStoresNothingMore(t *testing.T) {
	m := newMemory()
	svc := newTestService(m)
	if _, err := svc.Open(context.Background(), player, brl(t, "100.00")); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	wallets, transactions, entries, events := len(m.wallets), len(m.transactions), len(m.entries), len(m.events)

	_, err := svc.Open(context.Background(), player, brl(t, "500.00"))

	if !errors.Is(err, walletiface.ErrAlreadyExists) {
		t.Fatalf("error = %v, want ErrAlreadyExists", err)
	}
	if len(m.wallets) != wallets || len(m.transactions) != transactions || len(m.entries) != entries || len(m.events) != events {
		t.Fatal("a refused opening left something behind")
	}
}

func TestAWalletInAnotherCurrencyForTheSamePlayerIsAccepted(t *testing.T) {
	m := newMemory()
	svc := newTestService(m)
	if _, err := svc.Open(context.Background(), player, brl(t, "0.00")); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	usd, err := entities.ParseMoney("0.00", "USD")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Open(context.Background(), player, usd); err != nil {
		t.Fatalf("a wallet in another currency must be accepted: %v", err)
	}
	if len(m.wallets) != 2 {
		t.Fatalf("stored %d wallets, want 2", len(m.wallets))
	}
}

func TestAFailureHalfwayThroughRollsTheWholeOpeningBack(t *testing.T) {
	m := newMemory()
	m.failOutbox = persistenceiface.ErrUnavailable

	_, err := newTestService(m).Open(context.Background(), player, brl(t, "1000.00"))

	if !errors.Is(err, persistenceiface.ErrUnavailable) {
		t.Fatalf("error = %v, want the failure of the outbox", err)
	}
	if len(m.wallets) != 0 || len(m.transactions) != 0 || len(m.entries) != 0 || len(m.events) != 0 {
		t.Fatalf("a wallet, a transaction or an entry survived an event that could not be written: %d, %d, %d, %d",
			len(m.wallets), len(m.transactions), len(m.entries), len(m.events))
	}
}

func TestInvalidInputNeverReachesTheStorage(t *testing.T) {
	negative, err := entities.NewMoney(-100, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		player  uuid.UUID
		initial entities.Money
	}{
		"a negative balance":      {player, negative},
		"an uninitialised amount": {player, entities.Money{}},
		"no player":               {uuid.Nil, brl(t, "10.00")},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := newMemory()

			_, err := newTestService(m).Open(context.Background(), tc.player, tc.initial)

			if !errors.Is(err, entities.ErrInvalidWallet) {
				t.Fatalf("error = %v, want ErrInvalidWallet", err)
			}
			if m.atomicCalls != 0 {
				t.Fatalf("a unit of work was opened for input that was invalid before it started")
			}
		})
	}
}

func TestTheServiceBuiltForProductionUsesRealClockAndIds(t *testing.T) {
	built, ok := NewService(unitOfWork{newMemory()}, walletStore{newMemory()}, wageringStore{newMemory()},
		outboxStore{newMemory()}, observability.NewNop()).(*service)
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
