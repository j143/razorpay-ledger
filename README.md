# razorpay-ledger

Skeleton implementation of a Razorpay-style transaction ledger with idempotent
payment APIs, concurrent-safe refunds, and idempotent webhook processing.

## Architecture overview

```
cmd/server/          ← HTTP server entry-point
api/                 ← HTTP handlers (routes, request parsing, JSON responses)
internal/
  ledger/            ← Double-entry bookkeeping (PostTransaction, GetStatement)
  payment/           ← Idempotent payment creation and capture
  refund/            ← Concurrent-safe, idempotent refund processing
  webhook/           ← Idempotent inbound webhook deduplication & dispatch
pkg/
  models/            ← Domain types (Account, JournalEntry, Payment, Refund, …)
  store/             ← In-memory store with optimistic locking (swap for Postgres)
```

## Design answers

| Problem | Solution |
|---------|----------|
| **Ledger for all money movements** | Double-entry bookkeeping: every movement is a balanced `Transaction` with ≥2 `JournalEntry` rows — one DEBIT, one CREDIT. Money is never created or destroyed. |
| **Idempotent payment APIs** | Every request carries an `idempotency_key`. Before processing, the service checks for an existing record with that key. If found, the existing result is returned unchanged. Key + record are written atomically. |
| **Two simultaneous refunds** | A per-payment mutex (analogous to `SELECT … FOR UPDATE`) serialises concurrent refund attempts. An over-refund guard rejects any refund that would exceed the original payment amount. Each refund also carries its own idempotency key for retries. |
| **Webhook retried 10 times** | Each webhook carries a globally-unique `event_id`. On arrival the processor attempts to INSERT the event_id into a deduplication table. A duplicate-key error means the event was already seen — respond 200 OK without invoking the downstream handler again. |

## Quick start

```bash
go run ./cmd/server          # start on :8080

# Create accounts
curl -s -XPOST localhost:8080/v1/accounts \
  -d '{"id":"cust-1","name":"Alice","currency":"INR","balance":10000}'

curl -s -XPOST localhost:8080/v1/accounts \
  -d '{"id":"merch-1","name":"TechStore","currency":"INR","balance":0}'

# Create a payment (idempotent — safe to replay)
curl -s -XPOST localhost:8080/v1/payments \
  -d '{"id":"pay-1","idempotency_key":"order-42","amount":2000,"currency":"INR","customer_id":"cust-1","merchant_id":"merch-1"}'

# Capture the payment
curl -s -XPOST localhost:8080/v1/payments/pay-1/capture \
  -d '{"txn_id":"txn-1","customer_account_id":"cust-1","merchant_account_id":"merch-1"}'

# Refund (idempotent, concurrent-safe)
curl -s -XPOST localhost:8080/v1/refunds \
  -d '{"id":"ref-1","idempotency_key":"refund-42","payment_id":"pay-1","amount":500,"txn_id":"txn-ref-1","merchant_account_id":"merch-1","customer_account_id":"cust-1"}'

# Ingest a webhook (retrying with the same event_id is safe)
curl -s -XPOST localhost:8080/v1/webhooks/events \
  -d '{"event_id":"evt-1","event_type":"payment.captured","payload":"{}","source":"bank"}'
```

## Testing

```bash
go test ./... -race -v
```

All 18 tests pass with the Go race detector enabled.

## Further reading

- [ROADMAP.md](ROADMAP.md) — phased plan from skeleton to production
- [LEARNING.md](LEARNING.md) — learning goals and AI-assisted exercises
