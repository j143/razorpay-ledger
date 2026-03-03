// Package store provides the in-memory storage backend used by all services.
//
// In production this would be backed by a PostgreSQL database with row-level
// locking (SELECT … FOR UPDATE) and serialisable transactions to guarantee
// consistency under concurrent load.  The in-memory implementation here uses
// Go mutexes to demonstrate the same concurrency guarantees in unit tests.
package store

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/j143/razorpay-ledger/pkg/models"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("record not found")

// ErrConflict is returned when an idempotency key already exists.
var ErrConflict = errors.New("record already exists")

// ErrOptimisticLock is returned when an optimistic-lock version mismatch
// occurs (another writer modified the row concurrently).
var ErrOptimisticLock = errors.New("optimistic lock conflict: please retry")

// ErrInsufficientFunds is returned when a debit would make a balance negative.
var ErrInsufficientFunds = errors.New("insufficient funds")

// Store is the central in-memory data store.
type Store struct {
	mu sync.Mutex

	accounts    map[string]*models.Account
	entries     map[string]*models.JournalEntry
	transactions map[string]*models.Transaction
	payments    map[string]*models.Payment // keyed by payment ID
	paymentKeys map[string]string          // idempotency_key → payment ID
	refunds     map[string]*models.Refund  // keyed by refund ID
	refundKeys  map[string]string          // idempotency_key → refund ID
	refundsByPayment map[string][]string   // payment ID → []refund ID
	webhookEvents map[string]*models.WebhookEvent // event_id → event
}

// New creates an initialised, empty Store.
func New() *Store {
	return &Store{
		accounts:         make(map[string]*models.Account),
		entries:          make(map[string]*models.JournalEntry),
		transactions:     make(map[string]*models.Transaction),
		payments:         make(map[string]*models.Payment),
		paymentKeys:      make(map[string]string),
		refunds:          make(map[string]*models.Refund),
		refundKeys:       make(map[string]string),
		refundsByPayment: make(map[string][]string),
		webhookEvents:    make(map[string]*models.WebhookEvent),
	}
}

// ------------------------------------------------------------------ Accounts

// CreateAccount persists a new account.
func (s *Store) CreateAccount(a *models.Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.accounts[a.ID]; exists {
		return fmt.Errorf("account %s: %w", a.ID, ErrConflict)
	}
	a.Version = 1
	a.CreatedAt = time.Now().UTC()
	a.UpdatedAt = a.CreatedAt
	cp := *a
	s.accounts[a.ID] = &cp
	return nil
}

// GetAccount returns an account by ID.
func (s *Store) GetAccount(id string) (*models.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[id]
	if !ok {
		return nil, fmt.Errorf("account %s: %w", id, ErrNotFound)
	}
	cp := *a
	return &cp, nil
}

// UpdateAccountBalance applies a balance delta using optimistic locking.
//
// expectedVersion must match the current version; otherwise ErrOptimisticLock
// is returned so the caller can reload and retry.
func (s *Store) UpdateAccountBalance(id string, delta int64, expectedVersion int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.accounts[id]
	if !ok {
		return fmt.Errorf("account %s: %w", id, ErrNotFound)
	}
	if a.Version != expectedVersion {
		return ErrOptimisticLock
	}
	newBalance := a.Balance + delta
	if newBalance < 0 {
		return ErrInsufficientFunds
	}
	a.Balance = newBalance
	a.Version++
	a.UpdatedAt = time.Now().UTC()
	return nil
}

// ------------------------------------------------------------------ Ledger entries

// CreateJournalEntry persists a journal entry.
func (s *Store) CreateJournalEntry(e *models.JournalEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.CreatedAt = time.Now().UTC()
	cp := *e
	s.entries[e.ID] = &cp
	return nil
}

// ListEntriesByAccount returns all journal entries for an account.
func (s *Store) ListEntriesByAccount(accountID string) ([]*models.JournalEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*models.JournalEntry
	for _, e := range s.entries {
		if e.AccountID == accountID {
			cp := *e
			out = append(out, &cp)
		}
	}
	return out, nil
}

// CreateTransaction persists a balanced transaction (with its entries).
func (s *Store) CreateTransaction(t *models.Transaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.CreatedAt = time.Now().UTC()
	cp := *t
	s.transactions[t.ID] = &cp
	return nil
}

// ------------------------------------------------------------------ Payments

// CreatePayment persists a new payment, enforcing idempotency-key uniqueness.
func (s *Store) CreatePayment(p *models.Payment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existingID, dup := s.paymentKeys[p.IdempotencyKey]; dup {
		return fmt.Errorf("idempotency_key %q already used by payment %s: %w",
			p.IdempotencyKey, existingID, ErrConflict)
	}
	p.CreatedAt = time.Now().UTC()
	p.UpdatedAt = p.CreatedAt
	cp := *p
	s.payments[p.ID] = &cp
	s.paymentKeys[p.IdempotencyKey] = p.ID
	return nil
}

// GetPayment returns a payment by ID.
func (s *Store) GetPayment(id string) (*models.Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.payments[id]
	if !ok {
		return nil, fmt.Errorf("payment %s: %w", id, ErrNotFound)
	}
	cp := *p
	return &cp, nil
}

// GetPaymentByIdempotencyKey returns the payment associated with a key (or
// ErrNotFound if none exists yet).
func (s *Store) GetPaymentByIdempotencyKey(key string) (*models.Payment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.paymentKeys[key]
	if !ok {
		return nil, ErrNotFound
	}
	p := s.payments[id]
	cp := *p
	return &cp, nil
}

// UpdatePaymentStatus sets the status of an existing payment.
func (s *Store) UpdatePaymentStatus(id string, status models.PaymentStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.payments[id]
	if !ok {
		return fmt.Errorf("payment %s: %w", id, ErrNotFound)
	}
	p.Status = status
	p.UpdatedAt = time.Now().UTC()
	return nil
}

// ------------------------------------------------------------------ Refunds

// CreateRefund persists a new refund, enforcing idempotency-key uniqueness.
// It also registers the refund against its parent payment.
func (s *Store) CreateRefund(r *models.Refund) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existingID, dup := s.refundKeys[r.IdempotencyKey]; dup {
		return fmt.Errorf("idempotency_key %q already used by refund %s: %w",
			r.IdempotencyKey, existingID, ErrConflict)
	}
	r.CreatedAt = time.Now().UTC()
	r.UpdatedAt = r.CreatedAt
	cp := *r
	s.refunds[r.ID] = &cp
	s.refundKeys[r.IdempotencyKey] = r.ID
	s.refundsByPayment[r.PaymentID] = append(s.refundsByPayment[r.PaymentID], r.ID)
	return nil
}

// GetRefund returns a refund by ID.
func (s *Store) GetRefund(id string) (*models.Refund, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.refunds[id]
	if !ok {
		return nil, fmt.Errorf("refund %s: %w", id, ErrNotFound)
	}
	cp := *r
	return &cp, nil
}

// GetRefundByIdempotencyKey returns the refund associated with a key.
func (s *Store) GetRefundByIdempotencyKey(key string) (*models.Refund, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.refundKeys[key]
	if !ok {
		return nil, ErrNotFound
	}
	r := s.refunds[id]
	cp := *r
	return &cp, nil
}

// ListRefundsByPayment returns all refunds for a given payment.
func (s *Store) ListRefundsByPayment(paymentID string) ([]*models.Refund, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.refundsByPayment[paymentID]
	out := make([]*models.Refund, 0, len(ids))
	for _, id := range ids {
		r := s.refunds[id]
		cp := *r
		out = append(out, &cp)
	}
	return out, nil
}

// UpdateRefundStatus sets the status of an existing refund.
func (s *Store) UpdateRefundStatus(id string, status models.RefundStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.refunds[id]
	if !ok {
		return fmt.Errorf("refund %s: %w", id, ErrNotFound)
	}
	r.Status = status
	r.UpdatedAt = time.Now().UTC()
	return nil
}

// ------------------------------------------------------------------ Webhook events

// CreateWebhookEvent persists a new webhook event, enforcing event_id uniqueness.
// Returns ErrConflict if the event_id was already processed (idempotency guard).
func (s *Store) CreateWebhookEvent(e *models.WebhookEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.webhookEvents[e.EventID]; dup {
		return fmt.Errorf("event_id %q: %w", e.EventID, ErrConflict)
	}
	e.ReceivedAt = time.Now().UTC()
	cp := *e
	s.webhookEvents[e.EventID] = &cp
	return nil
}

// GetWebhookEvent returns a webhook event by its event_id.
func (s *Store) GetWebhookEvent(eventID string) (*models.WebhookEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.webhookEvents[eventID]
	if !ok {
		return nil, fmt.Errorf("event %s: %w", eventID, ErrNotFound)
	}
	cp := *e
	return &cp, nil
}

// MarkWebhookEventProcessed stamps the event's ProcessedAt time.
func (s *Store) MarkWebhookEventProcessed(eventID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.webhookEvents[eventID]
	if !ok {
		return fmt.Errorf("event %s: %w", eventID, ErrNotFound)
	}
	now := time.Now().UTC()
	e.ProcessedAt = &now
	return nil
}
