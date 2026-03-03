// Package payment implements idempotent payment processing with at-least-once
// delivery semantics from upstream.
//
// # Idempotency design
//
// Every payment request carries a caller-supplied IdempotencyKey.  Before
// processing a new payment the service checks whether a payment with that key
// already exists:
//
//   - If yes → return the existing payment unchanged (idempotent).
//   - If no  → create and process the payment atomically.
//
// The key is stored alongside the payment record in the same atomic write,
// so two concurrent requests with the same key will race to insert; the loser
// receives ErrConflict and can safely retry, which will then hit the "already
// exists" branch.
//
// This pattern guarantees exactly-once side-effects even when the upstream
// retries the same request multiple times (at-least-once delivery).
package payment

import (
	"errors"
	"fmt"

	"github.com/j143/razorpay-ledger/internal/ledger"
	"github.com/j143/razorpay-ledger/pkg/models"
	"github.com/j143/razorpay-ledger/pkg/store"
)

// Service handles payment creation and capture.
type Service struct {
	store  *store.Store
	ledger *ledger.Service
}

// New creates a payment Service.
func New(s *store.Store, l *ledger.Service) *Service {
	return &Service{store: s, ledger: l}
}

// CreateOrGetPayment is the idempotent entry-point for creating a payment.
//
// If a payment with the given IdempotencyKey already exists the existing
// payment is returned without any side-effects (idempotent replay).
// Otherwise a new payment is created in PENDING state.
func (svc *Service) CreateOrGetPayment(p *models.Payment) (*models.Payment, error) {
	if p.IdempotencyKey == "" {
		return nil, errors.New("idempotency_key is required")
	}
	if p.Amount <= 0 {
		return nil, errors.New("amount must be positive")
	}
	if p.CustomerID == "" || p.MerchantID == "" {
		return nil, errors.New("customer_id and merchant_id are required")
	}

	// --- Idempotency check ---
	existing, err := svc.store.GetPaymentByIdempotencyKey(p.IdempotencyKey)
	if err == nil {
		// Already processed; return existing result (idempotent).
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("idempotency lookup: %w", err)
	}

	// --- New payment ---
	p.Status = models.PaymentPending
	if err := svc.store.CreatePayment(p); err != nil {
		// A concurrent request might have inserted the same key just now;
		// in that case, return the winner's record.
		if errors.Is(err, store.ErrConflict) {
			return svc.store.GetPaymentByIdempotencyKey(p.IdempotencyKey)
		}
		return nil, fmt.Errorf("creating payment: %w", err)
	}
	return p, nil
}

// CapturePayment moves a PENDING payment to CAPTURED and posts the double-entry
// ledger transaction that records the money movement.
//
// Accounts involved:
//
//	customer_id  DEBIT  amount   (money leaves the customer)
//	merchant_id  CREDIT amount   (money arrives at the merchant)
//
// In production a Razorpay fee entry would be interposed here.
func (svc *Service) CapturePayment(paymentID, txnID, customerAccountID, merchantAccountID string) (*models.Payment, error) {
	p, err := svc.store.GetPayment(paymentID)
	if err != nil {
		return nil, fmt.Errorf("payment not found: %w", err)
	}
	if p.Status != models.PaymentPending {
		return nil, fmt.Errorf("cannot capture payment in status %q", p.Status)
	}

	// Post the double-entry transaction.
	txn := &models.Transaction{
		ID:          txnID,
		Description: fmt.Sprintf("Capture for payment %s", paymentID),
		Entries: []models.JournalEntry{
			{
				ID:          txnID + "_debit",
				AccountID:   customerAccountID,
				Amount:      p.Amount,
				Currency:    p.Currency,
				Type:        models.Debit,
				ReferenceID: paymentID,
				Description: "Payment debit",
			},
			{
				ID:          txnID + "_credit",
				AccountID:   merchantAccountID,
				Amount:      p.Amount,
				Currency:    p.Currency,
				Type:        models.Credit,
				ReferenceID: paymentID,
				Description: "Payment credit",
			},
		},
	}
	if err := svc.ledger.PostTransaction(txn); err != nil {
		return nil, fmt.Errorf("posting ledger transaction: %w", err)
	}

	if err := svc.store.UpdatePaymentStatus(paymentID, models.PaymentCaptured); err != nil {
		return nil, fmt.Errorf("updating payment status: %w", err)
	}
	return svc.store.GetPayment(paymentID)
}
