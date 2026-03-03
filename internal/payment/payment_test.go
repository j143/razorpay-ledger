package payment_test

import (
	"sync"
	"testing"

	"github.com/j143/razorpay-ledger/internal/ledger"
	"github.com/j143/razorpay-ledger/internal/payment"
	"github.com/j143/razorpay-ledger/pkg/models"
	"github.com/j143/razorpay-ledger/pkg/store"
)

func setup() (*store.Store, *ledger.Service, *payment.Service) {
	s := store.New()
	l := ledger.New(s)
	p := payment.New(s, l)
	return s, l, p
}

func seedAccounts(t *testing.T, l *ledger.Service, accounts ...*models.Account) {
	t.Helper()
	for _, a := range accounts {
		if err := l.CreateAccount(a); err != nil {
			t.Fatalf("seed account %s: %v", a.ID, err)
		}
	}
}

func TestCreateOrGetPayment_NewPayment(t *testing.T) {
	_, l, svc := setup()
	seedAccounts(t, l,
		&models.Account{ID: "cust", Currency: models.INR, Balance: 5000},
		&models.Account{ID: "merch", Currency: models.INR, Balance: 0},
	)

	p := &models.Payment{
		ID:             "pay-1",
		IdempotencyKey: "idem-1",
		Amount:         1000,
		Currency:       models.INR,
		CustomerID:     "cust",
		MerchantID:     "merch",
	}
	result, err := svc.CreateOrGetPayment(p)
	if err != nil {
		t.Fatalf("CreateOrGetPayment: %v", err)
	}
	if result.Status != models.PaymentPending {
		t.Errorf("expected PENDING, got %s", result.Status)
	}
}

func TestCreateOrGetPayment_Idempotent(t *testing.T) {
	_, l, svc := setup()
	seedAccounts(t, l,
		&models.Account{ID: "cust", Currency: models.INR, Balance: 5000},
		&models.Account{ID: "merch", Currency: models.INR, Balance: 0},
	)

	p := &models.Payment{
		ID:             "pay-2",
		IdempotencyKey: "idem-2",
		Amount:         2000,
		Currency:       models.INR,
		CustomerID:     "cust",
		MerchantID:     "merch",
	}
	first, _ := svc.CreateOrGetPayment(p)

	// Second call with same idempotency_key must return the same record.
	second, err := svc.CreateOrGetPayment(p)
	if err != nil {
		t.Fatalf("second CreateOrGetPayment: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("idempotency broken: got different IDs %s vs %s", first.ID, second.ID)
	}
}

func TestCreateOrGetPayment_ConcurrentSameKey(t *testing.T) {
	_, l, svc := setup()
	seedAccounts(t, l,
		&models.Account{ID: "cust", Currency: models.INR, Balance: 10000},
		&models.Account{ID: "merch", Currency: models.INR, Balance: 0},
	)

	const goroutines = 20
	results := make([]*models.Payment, goroutines)
	errs := make([]error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = svc.CreateOrGetPayment(&models.Payment{
				ID:             "pay-concurrent",
				IdempotencyKey: "idem-concurrent",
				Amount:         500,
				Currency:       models.INR,
				CustomerID:     "cust",
				MerchantID:     "merch",
			})
		}(i)
	}
	wg.Wait()

	// All goroutines must get the same payment (no error).
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}
	first := results[0]
	for i, r := range results {
		if r == nil || r.ID != first.ID {
			t.Errorf("goroutine %d: expected payment %s, got %v", i, first.ID, r)
		}
	}
}

func TestCapturePayment(t *testing.T) {
	_, l, svc := setup()
	seedAccounts(t, l,
		&models.Account{ID: "cust", Currency: models.INR, Balance: 5000},
		&models.Account{ID: "merch", Currency: models.INR, Balance: 0},
	)

	p := &models.Payment{
		ID:             "pay-cap",
		IdempotencyKey: "idem-cap",
		Amount:         1000,
		Currency:       models.INR,
		CustomerID:     "cust",
		MerchantID:     "merch",
	}
	if _, err := svc.CreateOrGetPayment(p); err != nil {
		t.Fatalf("create: %v", err)
	}
	captured, err := svc.CapturePayment("pay-cap", "txn-cap", "cust", "merch")
	if err != nil {
		t.Fatalf("CapturePayment: %v", err)
	}
	if captured.Status != models.PaymentCaptured {
		t.Errorf("expected CAPTURED, got %s", captured.Status)
	}

	// Customer balance should decrease, merchant should increase.
	custAcct, _ := l.GetAccount("cust")
	merchAcct, _ := l.GetAccount("merch")
	if custAcct.Balance != 4000 {
		t.Errorf("customer balance: want 4000, got %d", custAcct.Balance)
	}
	if merchAcct.Balance != 1000 {
		t.Errorf("merchant balance: want 1000, got %d", merchAcct.Balance)
	}
}

func TestCapturePayment_AlreadyCaptured(t *testing.T) {
	_, l, svc := setup()
	seedAccounts(t, l,
		&models.Account{ID: "cust", Currency: models.INR, Balance: 5000},
		&models.Account{ID: "merch", Currency: models.INR, Balance: 0},
	)

	p := &models.Payment{
		ID:             "pay-double-cap",
		IdempotencyKey: "idem-double-cap",
		Amount:         1000,
		Currency:       models.INR,
		CustomerID:     "cust",
		MerchantID:     "merch",
	}
	if _, err := svc.CreateOrGetPayment(p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := svc.CapturePayment("pay-double-cap", "txn-dc1", "cust", "merch"); err != nil {
		t.Fatalf("first capture: %v", err)
	}
	_, err := svc.CapturePayment("pay-double-cap", "txn-dc2", "cust", "merch")
	if err == nil {
		t.Error("expected error on double capture, got nil")
	}
}

func TestCreateOrGetPayment_MissingIdempotencyKey(t *testing.T) {
	_, _, svc := setup()
	_, err := svc.CreateOrGetPayment(&models.Payment{
		ID:         "pay-no-key",
		Amount:     100,
		CustomerID: "x",
		MerchantID: "y",
	})
	if err == nil {
		t.Error("expected error for missing idempotency_key")
	}
}

func TestCreateOrGetPayment_ZeroAmount(t *testing.T) {
	_, _, svc := setup()
	_, err := svc.CreateOrGetPayment(&models.Payment{
		ID:             "pay-zero",
		IdempotencyKey: "k",
		Amount:         0,
		CustomerID:     "x",
		MerchantID:     "y",
	})
	if err == nil {
		t.Error("expected error for zero amount")
	}
}
