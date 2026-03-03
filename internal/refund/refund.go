// Package refund handles refund processing with protection against concurrent
// duplicate refunds arriving from different upstream channels.
//
// # Concurrency problem
//
// Two refund requests for the same payment can arrive almost simultaneously
// from two separate upstream systems (e.g. bank reconciliation + customer
// support tool).  Without explicit protection we would:
//
//  1. Read the payment – both goroutines see status CAPTURED.
//  2. Both decide to proceed and create a refund.
//  3. The customer receives two refunds.
//
// # Solution – per-payment mutex (simulating SELECT … FOR UPDATE)
//
// In a SQL database the canonical fix is to lock the parent payment row before
// reading it (SELECT … FOR UPDATE inside a serialisable transaction).  Here we
// replicate that with a per-payment mutex map so that only one refund attempt
// for a given paymentID proceeds at a time.
//
// Additionally every refund request carries an IdempotencyKey so that a retry
// of the *same* request (rather than a second, distinct refund) is safely
// deduplicated by returning the already-created refund unchanged.
//
// # Partial-refund guard
//
// The service also checks that the total amount already refunded plus the
// requested amount does not exceed the original payment amount, preventing
// over-refunds even across multiple partial refunds.
package refund

import (
	"errors"
	"fmt"
	"sync"

	"github.com/j143/razorpay-ledger/internal/ledger"
	"github.com/j143/razorpay-ledger/pkg/models"
	"github.com/j143/razorpay-ledger/pkg/store"
)

// Service handles refund creation with concurrency safety.
type Service struct {
	store  *store.Store
	ledger *ledger.Service

	// paymentLocks provides a per-payment mutex that serialises concurrent
	// refund attempts for the same payment – analogous to SELECT FOR UPDATE.
	mu           sync.Mutex
	paymentLocks map[string]*sync.Mutex
}

// New creates a refund Service.
func New(s *store.Store, l *ledger.Service) *Service {
	return &Service{
		store:        s,
		ledger:       l,
		paymentLocks: make(map[string]*sync.Mutex),
	}
}

// lockForPayment returns (and lazily creates) the per-payment mutex.
func (svc *Service) lockForPayment(paymentID string) *sync.Mutex {
	svc.mu.Lock()
	defer svc.mu.Unlock()
	if _, ok := svc.paymentLocks[paymentID]; !ok {
		svc.paymentLocks[paymentID] = &sync.Mutex{}
	}
	return svc.paymentLocks[paymentID]
}

// CreateOrGetRefund is the idempotent entry-point for initiating a refund.
//
// Behaviour:
//
//  1. Idempotency: if a refund with the same IdempotencyKey already exists,
//     return it unchanged.
//  2. Concurrency safety: acquire a per-payment lock before reading the
//     payment and the existing refunds – equivalent to SELECT … FOR UPDATE.
//  3. Over-refund guard: reject the request if it would exceed the payment
//     amount.
//  4. Status check: only CAPTURED payments can be refunded.
func (svc *Service) CreateOrGetRefund(r *models.Refund, txnID, merchantAccountID, customerAccountID string) (*models.Refund, error) {
	if r.IdempotencyKey == "" {
		return nil, errors.New("idempotency_key is required")
	}
	if r.Amount <= 0 {
		return nil, errors.New("amount must be positive")
	}

	// --- Idempotency check (fast path, no payment lock needed) ---
	existing, err := svc.store.GetRefundByIdempotencyKey(r.IdempotencyKey)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("idempotency lookup: %w", err)
	}

	// --- Acquire per-payment lock (SELECT … FOR UPDATE equivalent) ---
	paymentLock := svc.lockForPayment(r.PaymentID)
	paymentLock.Lock()
	defer paymentLock.Unlock()

	// Re-check idempotency after acquiring the lock to handle the race where
	// a concurrent request inserted the same key between our first check and
	// us acquiring the lock.
	existing, err = svc.store.GetRefundByIdempotencyKey(r.IdempotencyKey)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("idempotency re-check: %w", err)
	}

	// --- Validate payment status ---
	payment, err := svc.store.GetPayment(r.PaymentID)
	if err != nil {
		return nil, fmt.Errorf("payment %s not found: %w", r.PaymentID, err)
	}
	if payment.Status != models.PaymentCaptured {
		return nil, fmt.Errorf("cannot refund payment with status %q", payment.Status)
	}

	// --- Over-refund guard ---
	existingRefunds, err := svc.store.ListRefundsByPayment(r.PaymentID)
	if err != nil {
		return nil, fmt.Errorf("listing existing refunds: %w", err)
	}
	var alreadyRefunded int64
	for _, er := range existingRefunds {
		if er.Status == models.RefundProcessed || er.Status == models.RefundPending {
			alreadyRefunded += er.Amount
		}
	}
	if alreadyRefunded+r.Amount > payment.Amount {
		return nil, fmt.Errorf(
			"refund amount %d would exceed payment amount %d (already refunded: %d)",
			r.Amount, payment.Amount, alreadyRefunded,
		)
	}

	// --- Persist refund record ---
	r.Status = models.RefundPending
	r.Currency = payment.Currency
	if err := svc.store.CreateRefund(r); err != nil {
		// Concurrent insert with same idempotency key – return winner.
		if errors.Is(err, store.ErrConflict) {
			return svc.store.GetRefundByIdempotencyKey(r.IdempotencyKey)
		}
		return nil, fmt.Errorf("creating refund: %w", err)
	}

	// --- Post reverse ledger transaction ---
	// Refund reverses the original capture: money flows merchant → customer.
	txn := &models.Transaction{
		ID:          txnID,
		Description: fmt.Sprintf("Refund %s for payment %s", r.ID, r.PaymentID),
		Entries: []models.JournalEntry{
			{
				ID:          txnID + "_debit",
				AccountID:   merchantAccountID,
				Amount:      r.Amount,
				Currency:    r.Currency,
				Type:        models.Debit,
				ReferenceID: r.ID,
				Description: "Refund debit (merchant)",
			},
			{
				ID:          txnID + "_credit",
				AccountID:   customerAccountID,
				Amount:      r.Amount,
				Currency:    r.Currency,
				Type:        models.Credit,
				ReferenceID: r.ID,
				Description: "Refund credit (customer)",
			},
		},
	}
	if err := svc.ledger.PostTransaction(txn); err != nil {
		// Mark as failed so the record is visible for investigation.
		_ = svc.store.UpdateRefundStatus(r.ID, models.RefundFailed)
		return nil, fmt.Errorf("posting refund ledger transaction: %w", err)
	}

	_ = svc.store.UpdateRefundStatus(r.ID, models.RefundProcessed)

	// If the fully-refunded amount now equals the payment amount, mark the
	// payment as REFUNDED.
	if alreadyRefunded+r.Amount == payment.Amount {
		_ = svc.store.UpdatePaymentStatus(r.PaymentID, models.PaymentRefunded)
	}

	return svc.store.GetRefund(r.ID)
}
