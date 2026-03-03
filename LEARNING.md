# Learning Goals — Razorpay Ledger

This document maps every design decision in the codebase to a concrete
learning objective, and describes how AI tooling accelerates the learning loop.

---

## 1. Double-Entry Bookkeeping

**Goal:** Understand why financial systems use double-entry bookkeeping and how
to implement it in code.

**Key concepts:**
- Every money movement creates at least two entries: one DEBIT, one CREDIT.
- The sum of debits must equal the sum of credits (the accounting equation).
- An immutable journal of entries provides a full audit trail.

**Where to look:**  
`pkg/models/models.go` → `JournalEntry`, `Transaction`  
`internal/ledger/ledger.go` → `PostTransaction`

**Exercise:** Add a Razorpay fee entry to `CapturePayment` so that 2% of each
payment is credited to a `razorpay_fees` account.

---

## 2. Idempotent APIs

**Goal:** Design APIs that are safe to call multiple times with the same input.

**Key concepts:**
- At-least-once delivery from upstream is the norm; your service must absorb
  duplicates without side-effects.
- An idempotency key uniquely identifies a logical operation.  Storing it
  atomically with the result is the foundation of an idempotent API.
- The pattern: `GET by key → if found return existing; else INSERT and process`.

**Where to look:**  
`internal/payment/payment.go` → `CreateOrGetPayment`  
`pkg/store/store.go` → `paymentKeys` map and `CreatePayment`

**Exercise:** Implement `CreateOrGetRefund` idempotency entirely inside a
PostgreSQL transaction using `INSERT … ON CONFLICT DO NOTHING RETURNING`.

---

## 3. Concurrent Refund Safety

**Goal:** Prevent two simultaneous refunds from double-debiting a payment.

**Key concepts:**
- Race condition: two goroutines both read `CAPTURED` status and both proceed.
- Fix 1 (in-memory): per-entity mutex (implemented here as `paymentLocks`).
- Fix 2 (database): `SELECT … FOR UPDATE` inside a DB transaction serialises
  concurrent writers for the same row.
- Fix 3 (optimistic lock): version field on the payment row; the second writer
  detects the stale version and retries.
- Over-refund guard: sum of existing refund amounts must not exceed the
  original payment amount.

**Where to look:**  
`internal/refund/refund.go` → `lockForPayment`, `CreateOrGetRefund`  
`internal/refund/refund_test.go` → `TestCreateOrGetRefund_ConcurrentDuplicates`

**Exercise:** Replace the mutex with a PostgreSQL advisory lock
(`pg_try_advisory_xact_lock(payment_id)`) to achieve the same guarantee across
multiple service replicas.

---

## 4. Idempotent Webhook Processing

**Goal:** Ensure downstream handlers are invoked exactly once even when a
webhook is retried 10+ times.

**Key concepts:**
- The upstream delivers events with at-least-once semantics; your consumer
  must implement at-most-once processing.
- Store the `event_id` in a `webhook_events` table with a UNIQUE constraint.
- Attempt to INSERT before invoking the handler.  A duplicate-key error means
  the event was already processed; return 200 OK without calling the handler.
- Never mark an event as `processed` if the handler failed — this allows a
  separate retry mechanism (e.g. DLQ) to reprocess it.

**Where to look:**  
`internal/webhook/webhook.go` → `Process`  
`internal/webhook/webhook_test.go` → `TestProcess_Idempotent`, `TestProcess_ConcurrentRetries`

**Exercise:** Add an exponential-back-off internal retry for handler failures
(up to 5 attempts), and only move to DLQ after all retries are exhausted.

---

## 5. Optimistic Locking

**Goal:** Understand how to prevent lost updates without pessimistic locks.

**Key concepts:**
- Each row carries a `version` integer.
- Update: `WHERE id = ? AND version = expected_version` and increment version.
- If 0 rows updated → another writer won; the caller retries from scratch.
- Suitable for low-contention workloads; for high contention prefer `SELECT
  FOR UPDATE`.

**Where to look:**  
`pkg/store/store.go` → `UpdateAccountBalance`

---

## AI as Primary Learning Accelerator

This project is designed to be explored and extended with the help of AI
assistants (GitHub Copilot, Claude, ChatGPT).  Suggested prompts:

| Goal | Prompt |
|------|--------|
| Understand a function | "Explain `PostTransaction` in ledger.go and give me an example that would fail the balance check." |
| Generate a test | "Write a table-driven Go test for `CreateOrGetRefund` that covers partial refunds, full refunds, and over-refund rejection." |
| Extend the design | "Add a `fee_account_id` parameter to `CapturePayment` and update the ledger entries to deduct a 2% Razorpay fee." |
| Explore the race | "Simulate the concurrent refund race condition by removing the `paymentLock.Lock()` call and running the test 1000 times. What happens?" |
| Production path | "Rewrite `pkg/store` to use `database/sql` with PostgreSQL and use `SELECT FOR UPDATE` instead of the in-memory mutex." |
| AI fraud scoring | "Design a `FraudScorer` interface in Go that takes a `*models.Payment` and returns a risk score (0–1). Mock it in tests with a simple threshold scorer and sketch how a real ML model would implement it." |

### Recommended Learning Sequence

1. Read `pkg/models/models.go` — understand the domain types.
2. Run `go test ./... -v -race` and study the output.
3. Trace a full payment lifecycle: `CreateOrGetPayment` → `CapturePayment` →
   `CreateOrGetRefund` through the source and tests.
4. Study `TestProcess_Idempotent` to see the webhook deduplication in action.
5. Try the exercises above, using AI to generate the boilerplate.
6. Read the ROADMAP and pick a Phase 1 task to implement.
