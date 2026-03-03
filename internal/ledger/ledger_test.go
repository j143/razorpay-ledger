package ledger_test

import (
	"testing"

	"github.com/j143/razorpay-ledger/internal/ledger"
	"github.com/j143/razorpay-ledger/pkg/models"
	"github.com/j143/razorpay-ledger/pkg/store"
)

func newTestStore() *store.Store { return store.New() }

func seedAccounts(t *testing.T, svc *ledger.Service, accounts ...*models.Account) {
	t.Helper()
	for _, a := range accounts {
		if err := svc.CreateAccount(a); err != nil {
			t.Fatalf("seeding account %s: %v", a.ID, err)
		}
	}
}

func TestPostTransaction_Balanced(t *testing.T) {
	s := newTestStore()
	svc := ledger.New(s)

	customer := &models.Account{ID: "cust-1", Name: "Customer", Currency: models.INR, Balance: 1000}
	merchant := &models.Account{ID: "merch-1", Name: "Merchant", Currency: models.INR, Balance: 0}
	seedAccounts(t, svc, customer, merchant)

	txn := &models.Transaction{
		ID:          "txn-1",
		Description: "test payment",
		Entries: []models.JournalEntry{
			{ID: "e1", AccountID: "cust-1", Amount: 500, Currency: models.INR, Type: models.Debit, ReferenceID: "pay-1"},
			{ID: "e2", AccountID: "merch-1", Amount: 500, Currency: models.INR, Type: models.Credit, ReferenceID: "pay-1"},
		},
	}
	if err := svc.PostTransaction(txn); err != nil {
		t.Fatalf("PostTransaction: %v", err)
	}

	// Customer balance should be 500, merchant 500.
	c, _ := svc.GetAccount("cust-1")
	m, _ := svc.GetAccount("merch-1")
	if c.Balance != 500 {
		t.Errorf("customer balance: want 500, got %d", c.Balance)
	}
	if m.Balance != 500 {
		t.Errorf("merchant balance: want 500, got %d", m.Balance)
	}
}

func TestPostTransaction_Unbalanced(t *testing.T) {
	s := newTestStore()
	svc := ledger.New(s)
	seedAccounts(t, svc,
		&models.Account{ID: "a", Currency: models.INR, Balance: 1000},
		&models.Account{ID: "b", Currency: models.INR, Balance: 1000},
	)

	txn := &models.Transaction{
		ID: "txn-bad",
		Entries: []models.JournalEntry{
			{ID: "e1", AccountID: "a", Amount: 500, Currency: models.INR, Type: models.Debit},
			{ID: "e2", AccountID: "b", Amount: 300, Currency: models.INR, Type: models.Credit}, // mismatch
		},
	}
	if err := svc.PostTransaction(txn); err == nil {
		t.Error("expected error for unbalanced transaction, got nil")
	}
}

func TestPostTransaction_InsufficientFunds(t *testing.T) {
	s := newTestStore()
	svc := ledger.New(s)
	seedAccounts(t, svc,
		&models.Account{ID: "poor", Currency: models.INR, Balance: 100},
		&models.Account{ID: "rich", Currency: models.INR, Balance: 0},
	)

	txn := &models.Transaction{
		ID: "txn-broke",
		Entries: []models.JournalEntry{
			{ID: "e1", AccountID: "poor", Amount: 500, Currency: models.INR, Type: models.Debit},
			{ID: "e2", AccountID: "rich", Amount: 500, Currency: models.INR, Type: models.Credit},
		},
	}
	if err := svc.PostTransaction(txn); err == nil {
		t.Error("expected insufficient funds error, got nil")
	}
}

func TestPostTransaction_TooFewEntries(t *testing.T) {
	s := newTestStore()
	svc := ledger.New(s)
	seedAccounts(t, svc, &models.Account{ID: "a", Currency: models.INR, Balance: 1000})

	txn := &models.Transaction{
		ID: "txn-single",
		Entries: []models.JournalEntry{
			{ID: "e1", AccountID: "a", Amount: 100, Currency: models.INR, Type: models.Debit},
		},
	}
	if err := svc.PostTransaction(txn); err == nil {
		t.Error("expected error for single-entry transaction, got nil")
	}
}

func TestGetAccountStatement(t *testing.T) {
	s := newTestStore()
	svc := ledger.New(s)
	seedAccounts(t, svc,
		&models.Account{ID: "c", Currency: models.INR, Balance: 1000},
		&models.Account{ID: "m", Currency: models.INR, Balance: 0},
	)

	txn := &models.Transaction{
		ID: "txn-stmt",
		Entries: []models.JournalEntry{
			{ID: "se1", AccountID: "c", Amount: 300, Currency: models.INR, Type: models.Debit, ReferenceID: "ref1"},
			{ID: "se2", AccountID: "m", Amount: 300, Currency: models.INR, Type: models.Credit, ReferenceID: "ref1"},
		},
	}
	if err := svc.PostTransaction(txn); err != nil {
		t.Fatalf("PostTransaction: %v", err)
	}

	entries, err := svc.GetAccountStatement("c")
	if err != nil {
		t.Fatalf("GetAccountStatement: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("want 1 entry, got %d", len(entries))
	}
}
