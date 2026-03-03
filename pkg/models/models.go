// Package models defines the core domain types for the Razorpay ledger.
package models

import (
	"time"
)

// Currency represents an ISO 4217 currency code (e.g. "INR", "USD").
type Currency string

const (
	INR Currency = "INR"
	USD Currency = "USD"
)

// EntryType distinguishes debit from credit ledger entries.
type EntryType string

const (
	Debit  EntryType = "DEBIT"
	Credit EntryType = "CREDIT"
)

// PaymentStatus is the lifecycle state of a payment.
type PaymentStatus string

const (
	PaymentPending   PaymentStatus = "PENDING"
	PaymentCaptured  PaymentStatus = "CAPTURED"
	PaymentFailed    PaymentStatus = "FAILED"
	PaymentRefunded  PaymentStatus = "REFUNDED"
)

// RefundStatus is the lifecycle state of a refund.
type RefundStatus string

const (
	RefundPending   RefundStatus = "PENDING"
	RefundProcessed RefundStatus = "PROCESSED"
	RefundFailed    RefundStatus = "FAILED"
)

// Account represents a ledger account (customer wallet, merchant account, etc.).
type Account struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Currency  Currency  `json:"currency"`
	Balance   int64     `json:"balance"` // amount in smallest unit (paise for INR)
	Version   int64     `json:"version"` // optimistic-lock version
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// JournalEntry is one leg of a double-entry transaction.
type JournalEntry struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"account_id"`
	Amount      int64     `json:"amount"` // always positive; direction is EntryType
	Currency    Currency  `json:"currency"`
	Type        EntryType `json:"type"`
	ReferenceID string    `json:"reference_id"` // payment / refund / payout ID
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// Transaction groups two or more journal entries that must balance.
type Transaction struct {
	ID          string         `json:"id"`
	Entries     []JournalEntry `json:"entries"`
	Description string         `json:"description"`
	CreatedAt   time.Time      `json:"created_at"`
}

// Payment represents a payment initiated by a customer.
type Payment struct {
	ID             string        `json:"id"`
	IdempotencyKey string        `json:"idempotency_key"`
	Amount         int64         `json:"amount"`
	Currency       Currency      `json:"currency"`
	CustomerID     string        `json:"customer_id"`
	MerchantID     string        `json:"merchant_id"`
	Status         PaymentStatus `json:"status"`
	Description    string        `json:"description"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// Refund represents a refund against a captured payment.
type Refund struct {
	ID             string       `json:"id"`
	IdempotencyKey string       `json:"idempotency_key"`
	PaymentID      string       `json:"payment_id"`
	Amount         int64        `json:"amount"`
	Currency       Currency     `json:"currency"`
	Status         RefundStatus `json:"status"`
	Reason         string       `json:"reason"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
}

// WebhookEvent represents an inbound event from an upstream system.
type WebhookEvent struct {
	EventID   string    `json:"event_id"`   // globally unique ID from the upstream system
	EventType string    `json:"event_type"` // e.g. "payment.captured"
	Payload   string    `json:"payload"`    // JSON payload (opaque to the idempotency layer)
	Source    string    `json:"source"`     // upstream system identifier
	ReceivedAt time.Time `json:"received_at"`
	ProcessedAt *time.Time `json:"processed_at,omitempty"`
}
