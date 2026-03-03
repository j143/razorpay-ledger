// Package ledger implements double-entry bookkeeping for all Razorpay money
// movements.
//
// # Design
//
// Every money movement is recorded as a balanced Transaction that contains at
// least two JournalEntries: one DEBIT (money leaves) and one CREDIT (money
// arrives).  The sum of all debits must equal the sum of all credits inside a
// transaction, preserving the accounting equation at all times.
//
// Example – payment capture flow:
//
//	Customer wallet  DEBIT  ₹500  →  Razorpay suspense  CREDIT  ₹500
//	Razorpay suspense DEBIT ₹500  →  Merchant wallet    CREDIT  ₹500
//
// This ensures that money is never created or destroyed; it only moves between
// accounts.
package ledger

import (
	"errors"
	"fmt"

	"github.com/j143/razorpay-ledger/pkg/models"
	"github.com/j143/razorpay-ledger/pkg/store"
)

// Service provides double-entry ledger operations.
type Service struct {
	store *store.Store
}

// New creates a ledger Service backed by the given store.
func New(s *store.Store) *Service {
	return &Service{store: s}
}

// CreateAccount creates a new ledger account (e.g. customer wallet, merchant
// settlement account, Razorpay fee account).
func (svc *Service) CreateAccount(a *models.Account) error {
	if a.ID == "" {
		return errors.New("account ID is required")
	}
	if a.Currency == "" {
		return errors.New("account currency is required")
	}
	return svc.store.CreateAccount(a)
}

// GetAccount returns an account by ID.
func (svc *Service) GetAccount(id string) (*models.Account, error) {
	return svc.store.GetAccount(id)
}

// PostTransaction validates and persists a balanced double-entry transaction.
//
// Rules:
//  1. A transaction must have at least two entries.
//  2. Total debits must equal total credits (balanced).
//  3. No account may go below zero (enforced per-entry via the store).
func (svc *Service) PostTransaction(t *models.Transaction) error {
	if len(t.Entries) < 2 {
		return errors.New("a transaction must have at least two journal entries")
	}

	// Validate balance: sum(debits) == sum(credits).
	var totalDebit, totalCredit int64
	for _, e := range t.Entries {
		if e.Amount <= 0 {
			return fmt.Errorf("entry %s: amount must be positive", e.ID)
		}
		switch e.Type {
		case models.Debit:
			totalDebit += e.Amount
		case models.Credit:
			totalCredit += e.Amount
		default:
			return fmt.Errorf("entry %s: unknown entry type %q", e.ID, e.Type)
		}
	}
	if totalDebit != totalCredit {
		return fmt.Errorf("unbalanced transaction: debits=%d credits=%d", totalDebit, totalCredit)
	}

	// Persist each entry and update account balances.
	for _, e := range t.Entries {
		entryRef := e
		if err := svc.store.CreateJournalEntry(&entryRef); err != nil {
			return fmt.Errorf("persisting entry %s: %w", e.ID, err)
		}
		// Debit reduces balance; credit increases balance.
		acct, err := svc.store.GetAccount(e.AccountID)
		if err != nil {
			return fmt.Errorf("account %s not found for entry %s: %w", e.AccountID, e.ID, err)
		}
		var delta int64
		if e.Type == models.Debit {
			delta = -e.Amount
		} else {
			delta = e.Amount
		}
		if err := svc.store.UpdateAccountBalance(e.AccountID, delta, acct.Version); err != nil {
			return fmt.Errorf("updating balance for account %s: %w", e.AccountID, err)
		}
	}

	return svc.store.CreateTransaction(t)
}

// GetAccountStatement returns all journal entries for an account, providing a
// full audit trail of every money movement.
func (svc *Service) GetAccountStatement(accountID string) ([]*models.JournalEntry, error) {
	return svc.store.ListEntriesByAccount(accountID)
}
