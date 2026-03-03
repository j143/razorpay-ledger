package refund_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/j143/razorpay-ledger/internal/ledger"
	"github.com/j143/razorpay-ledger/internal/payment"
	"github.com/j143/razorpay-ledger/internal/refund"
	"github.com/j143/razorpay-ledger/pkg/models"
	"github.com/j143/razorpay-ledger/pkg/store"
)

// setup creates all services and pre-seeds a captured payment ready for
// refunding.
func setup(t *testing.T) (*store.Store, *ledger.Service, *payment.Service, *refund.Service, *models.Payment) {
	t.Helper()
	s := store.New()
	l := ledger.New(s)
	p := payment.New(s, l)
	r := refund.New(s, l)

	// Seed accounts.
	for _, a := range []*models.Account{
		{ID: "cust", Currency: models.INR, Balance: 10000},
		{ID: "merch", Currency: models.INR, Balance: 0},
	} {
		if err := l.CreateAccount(a); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}

	// Create and capture a payment.
	pay := &models.Payment{
		ID:             "pay-refund-base",
		IdempotencyKey: "idem-refund-base",
		Amount:         2000,
		Currency:       models.INR,
		CustomerID:     "cust",
		MerchantID:     "merch",
	}
	if _, err := p.CreateOrGetPayment(pay); err != nil {
		t.Fatalf("create payment: %v", err)
	}
	captured, err := p.CapturePayment("pay-refund-base", "txn-capture", "cust", "merch")
	if err != nil {
		t.Fatalf("capture payment: %v", err)
	}
	return s, l, p, r, captured
}

func TestCreateOrGetRefund_Success(t *testing.T) {
	_, l, _, svc, pay := setup(t)

	ref := &models.Refund{
		ID:             "ref-1",
		IdempotencyKey: "idem-ref-1",
		PaymentID:      pay.ID,
		Amount:         500,
		Reason:         "customer request",
	}
	result, err := svc.CreateOrGetRefund(ref, "txn-ref-1", "merch", "cust")
	if err != nil {
		t.Fatalf("CreateOrGetRefund: %v", err)
	}
	if result.Status != models.RefundProcessed {
		t.Errorf("expected PROCESSED, got %s", result.Status)
	}

	// Balances: customer was debited 2000, then credited 500 → 8500.
	// Merchant was credited 2000, then debited 500 → 1500.
	custAcct, _ := l.GetAccount("cust")
	merchAcct, _ := l.GetAccount("merch")
	if custAcct.Balance != 8500 {
		t.Errorf("customer balance: want 8500, got %d", custAcct.Balance)
	}
	if merchAcct.Balance != 1500 {
		t.Errorf("merchant balance: want 1500, got %d", merchAcct.Balance)
	}
}

func TestCreateOrGetRefund_Idempotent(t *testing.T) {
	_, _, _, svc, pay := setup(t)

	ref := &models.Refund{
		ID:             "ref-idem",
		IdempotencyKey: "idem-ref-idem",
		PaymentID:      pay.ID,
		Amount:         300,
	}
	first, err := svc.CreateOrGetRefund(ref, "txn-ref-idem", "merch", "cust")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call with same key must return existing refund unchanged.
	second, err := svc.CreateOrGetRefund(ref, "txn-ref-idem-2", "merch", "cust")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("idempotency broken: %s vs %s", first.ID, second.ID)
	}
}

// TestCreateOrGetRefund_ConcurrentDuplicates simulates the scenario from the
// problem statement: two distinct refund requests (different idempotency keys,
// different sources) for the same payment arriving simultaneously.
//
// Expected behaviour: exactly one succeeds; the other is rejected because it
// would cause an over-refund (payment amount = 2000, each requests 2000).
func TestCreateOrGetRefund_ConcurrentDuplicates(t *testing.T) {
	_, _, _, svc, pay := setup(t)

	const goroutines = 10
	type outcome struct {
		ref *models.Refund
		err error
	}
	results := make([]outcome, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			// Each goroutine uses a unique idempotency key but the same
			// paymentID and the full payment amount – so only one can succeed.
			r, err := svc.CreateOrGetRefund(
				&models.Refund{
					ID:             fmt.Sprintf("ref-conc-%d", idx),
					IdempotencyKey: fmt.Sprintf("idem-conc-%d", idx),
					PaymentID:      pay.ID,
					Amount:         pay.Amount, // full refund – only one may succeed
					Reason:         "concurrent",
				},
				fmt.Sprintf("txn-conc-%d", idx),
				"merch",
				"cust",
			)
			results[idx] = outcome{r, err}
		}(i)
	}
	wg.Wait()

	successCount := 0
	for _, o := range results {
		if o.err == nil {
			successCount++
		}
	}
	if successCount != 1 {
		t.Errorf("expected exactly 1 successful refund, got %d", successCount)
	}
}

func TestCreateOrGetRefund_OverRefund(t *testing.T) {
	_, _, _, svc, pay := setup(t)

	// First refund: 1500 of 2000.
	_, err := svc.CreateOrGetRefund(
		&models.Refund{ID: "ref-over-1", IdempotencyKey: "idem-over-1", PaymentID: pay.ID, Amount: 1500},
		"txn-over-1", "merch", "cust",
	)
	if err != nil {
		t.Fatalf("first partial refund: %v", err)
	}

	// Second refund: 1000 would take total to 2500 → must be rejected.
	_, err = svc.CreateOrGetRefund(
		&models.Refund{ID: "ref-over-2", IdempotencyKey: "idem-over-2", PaymentID: pay.ID, Amount: 1000},
		"txn-over-2", "merch", "cust",
	)
	if err == nil {
		t.Error("expected over-refund error, got nil")
	}
}

func TestCreateOrGetRefund_NotCaptured(t *testing.T) {
	s := store.New()
	l := ledger.New(s)
	p := payment.New(s, l)
	r := refund.New(s, l)

	for _, a := range []*models.Account{
		{ID: "cust", Currency: models.INR, Balance: 5000},
		{ID: "merch", Currency: models.INR, Balance: 0},
	} {
		if err := l.CreateAccount(a); err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}

	// Create payment but do NOT capture it.
	if _, err := p.CreateOrGetPayment(&models.Payment{
		ID:             "pay-pending",
		IdempotencyKey: "idem-pending",
		Amount:         1000,
		Currency:       models.INR,
		CustomerID:     "cust",
		MerchantID:     "merch",
	}); err != nil {
		t.Fatalf("create payment: %v", err)
	}

	_, err := r.CreateOrGetRefund(
		&models.Refund{ID: "ref-pending", IdempotencyKey: "idem-ref-pending", PaymentID: "pay-pending", Amount: 500},
		"txn-ref-pending", "merch", "cust",
	)
	if err == nil {
		t.Error("expected error refunding a PENDING payment, got nil")
	}
}
