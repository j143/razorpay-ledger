// Package webhook implements an idempotent inbound webhook processor.
//
// # Problem
//
// Upstream systems deliver webhook events with at-least-once semantics.  A
// single logical event (e.g. "payment.captured") can arrive 10+ times due to
// network timeouts, load-balancer retries, or upstream queue redeliveries.
// Without an idempotency guard each retry would re-trigger downstream actions
// (send email, update order status, credit loyalty points, …), causing
// inconsistency and a poor customer experience.
//
// # Solution – event_id deduplication table
//
// Every inbound webhook carries a globally-unique event_id supplied by the
// upstream system.  Before dispatching the event to downstream handlers the
// processor:
//
//  1. Attempts to INSERT the event_id into a deduplication table.
//  2. If the INSERT succeeds   → process the event and mark it as processed.
//  3. If the INSERT conflicts  → the event was already seen; respond 200 OK
//     immediately without invoking any downstream handler.
//
// Responding 200 OK to duplicates is intentional: it stops the upstream from
// retrying, while the downstream receives the side-effect exactly once.
//
// # Dispatcher
//
// After deduplication, Process dispatches the event to a registered handler
// function keyed by EventType.  New event types can be registered without
// modifying the core processor.
package webhook

import (
	"errors"
	"fmt"

	"github.com/j143/razorpay-ledger/pkg/models"
	"github.com/j143/razorpay-ledger/pkg/store"
)

// HandlerFunc is the signature for an event-specific downstream handler.
type HandlerFunc func(event *models.WebhookEvent) error

// Processor deduplicates and dispatches inbound webhook events.
type Processor struct {
	store    *store.Store
	handlers map[string]HandlerFunc
}

// New creates a Processor backed by the given store.
func New(s *store.Store) *Processor {
	return &Processor{
		store:    s,
		handlers: make(map[string]HandlerFunc),
	}
}

// Register associates a HandlerFunc with an event type string.
// Multiple calls with the same eventType override the previous handler.
func (p *Processor) Register(eventType string, fn HandlerFunc) {
	p.handlers[eventType] = fn
}

// ProcessResult is returned by Process to communicate the outcome to the
// HTTP layer.
type ProcessResult struct {
	// Duplicate is true when this event_id was already processed.
	// The HTTP handler should still return 200 OK to stop upstream retries.
	Duplicate bool
	// Event is the stored (or pre-existing) webhook event record.
	Event *models.WebhookEvent
}

// Process is the idempotent entry-point for handling an inbound webhook.
//
// It guarantees that the downstream handler is called at most once per
// unique event_id, regardless of how many times the upstream retries.
func (p *Processor) Process(event *models.WebhookEvent) (*ProcessResult, error) {
	if event.EventID == "" {
		return nil, errors.New("event_id is required")
	}
	if event.EventType == "" {
		return nil, errors.New("event_type is required")
	}

	// --- Idempotency check via INSERT (atomic deduplication) ---
	err := p.store.CreateWebhookEvent(event)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			// Duplicate delivery – retrieve the original record and return.
			existing, lookupErr := p.store.GetWebhookEvent(event.EventID)
			if lookupErr != nil {
				return nil, fmt.Errorf("fetching duplicate event: %w", lookupErr)
			}
			return &ProcessResult{Duplicate: true, Event: existing}, nil
		}
		return nil, fmt.Errorf("persisting webhook event: %w", err)
	}

	// --- Dispatch to registered handler ---
	handler, ok := p.handlers[event.EventType]
	if !ok {
		// No handler registered; acknowledge the event to stop retries, but
		// log a warning in production.
		_ = p.store.MarkWebhookEventProcessed(event.EventID)
		return &ProcessResult{Duplicate: false, Event: event}, nil
	}

	if err := handler(event); err != nil {
		// Handler failed; do NOT mark as processed so the event can be
		// retried later by the internal retry mechanism.
		return nil, fmt.Errorf("handler for %q: %w", event.EventType, err)
	}

	if err := p.store.MarkWebhookEventProcessed(event.EventID); err != nil {
		return nil, fmt.Errorf("marking event processed: %w", err)
	}

	return &ProcessResult{Duplicate: false, Event: event}, nil
}
