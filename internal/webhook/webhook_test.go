package webhook_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/j143/razorpay-ledger/internal/webhook"
	"github.com/j143/razorpay-ledger/pkg/models"
	"github.com/j143/razorpay-ledger/pkg/store"
)

func newProcessor() *webhook.Processor {
	return webhook.New(store.New())
}

func TestProcess_NewEvent(t *testing.T) {
	p := newProcessor()
	p.Register("payment.captured", func(e *models.WebhookEvent) error {
		return nil
	})

	result, err := p.Process(&models.WebhookEvent{
		EventID:   "evt-1",
		EventType: "payment.captured",
		Payload:   `{"payment_id":"pay-1"}`,
		Source:    "razorpay",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if result.Duplicate {
		t.Error("expected non-duplicate, got Duplicate=true")
	}
}

// TestProcess_Idempotent simulates the webhook being retried 10 times.
// The handler must be called exactly once.
func TestProcess_Idempotent(t *testing.T) {
	p := newProcessor()
	callCount := 0
	p.Register("order.created", func(e *models.WebhookEvent) error {
		callCount++
		return nil
	})

	event := &models.WebhookEvent{
		EventID:   "evt-retry",
		EventType: "order.created",
		Payload:   `{"order_id":"ord-1"}`,
		Source:    "upstream",
	}

	const retries = 10
	for i := 0; i < retries; i++ {
		result, err := p.Process(event)
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if i == 0 && result.Duplicate {
			t.Error("first delivery must not be marked duplicate")
		}
		if i > 0 && !result.Duplicate {
			t.Errorf("retry %d: expected Duplicate=true", i)
		}
	}

	if callCount != 1 {
		t.Errorf("handler called %d times; want exactly 1", callCount)
	}
}

// TestProcess_ConcurrentRetries fires 20 concurrent goroutines with the same
// event_id.  The handler must be invoked exactly once.
func TestProcess_ConcurrentRetries(t *testing.T) {
	p := newProcessor()
	var mu sync.Mutex
	callCount := 0
	p.Register("refund.processed", func(e *models.WebhookEvent) error {
		mu.Lock()
		callCount++
		mu.Unlock()
		return nil
	})

	event := &models.WebhookEvent{
		EventID:   "evt-concurrent",
		EventType: "refund.processed",
		Payload:   `{"refund_id":"ref-1"}`,
		Source:    "bank",
	}

	const goroutines = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			_, _ = p.Process(event)
		}()
	}
	wg.Wait()

	if callCount != 1 {
		t.Errorf("handler called %d times under concurrent load; want exactly 1", callCount)
	}
}

func TestProcess_HandlerError_NotMarkedProcessed(t *testing.T) {
	s := store.New()
	p := webhook.New(s)
	p.Register("fail.event", func(e *models.WebhookEvent) error {
		return errors.New("downstream unavailable")
	})

	event := &models.WebhookEvent{
		EventID:   "evt-fail",
		EventType: "fail.event",
		Payload:   `{}`,
		Source:    "test",
	}
	_, err := p.Process(event)
	if err == nil {
		t.Fatal("expected error from handler, got nil")
	}

	// The event should exist in the store but NOT be marked processed, so it
	// can be retried by an internal retry mechanism.
	stored, lookupErr := s.GetWebhookEvent("evt-fail")
	if lookupErr != nil {
		t.Fatalf("event should be in store: %v", lookupErr)
	}
	if stored.ProcessedAt != nil {
		t.Error("event should not be marked processed after handler failure")
	}
}

func TestProcess_UnknownEventType_AcknowledgedWithoutHandler(t *testing.T) {
	p := newProcessor()
	// No handler registered for "unknown.event".

	result, err := p.Process(&models.WebhookEvent{
		EventID:   "evt-unknown",
		EventType: "unknown.event",
		Payload:   `{}`,
		Source:    "test",
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// Should succeed (deduplicated and acknowledged) even without a handler.
	if result.Duplicate {
		t.Error("first delivery must not be duplicate")
	}
}

func TestProcess_MissingEventID(t *testing.T) {
	p := newProcessor()
	_, err := p.Process(&models.WebhookEvent{EventType: "foo"})
	if err == nil {
		t.Error("expected error for missing event_id")
	}
}
