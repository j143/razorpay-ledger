# ROADMAP — Razorpay Ledger

This document outlines the planned evolution of the Razorpay Ledger from the
current skeleton through production-grade hardening.

---

## Phase 0 — Skeleton (✅ done)

| Item | Status |
|------|--------|
| Double-entry ledger (`internal/ledger`) | ✅ |
| Idempotent payment API (`internal/payment`) | ✅ |
| Concurrent-safe refund service (`internal/refund`) | ✅ |
| Idempotent webhook processor (`internal/webhook`) | ✅ |
| HTTP API (`api/handler.go`) | ✅ |
| In-memory store with optimistic locking (`pkg/store`) | ✅ |
| Unit tests with race-detector (`-race`) | ✅ |

---

## Phase 1 — Persistence & Schema

- [ ] Replace in-memory store with **PostgreSQL** backend
  - Migration framework (e.g. `golang-migrate`)
  - `accounts`, `journal_entries`, `transactions` tables
  - `payments`, `refunds`, `webhook_events` tables
  - `idempotency_key` unique index on `payments` and `refunds`
  - `event_id` unique index on `webhook_events`
- [ ] Replace per-payment mutex with `SELECT … FOR UPDATE` inside a DB
      transaction (row-level locking)
- [ ] Replace optimistic-lock version field with PostgreSQL `SERIALIZABLE`
      transaction isolation for ledger posts

---

## Phase 2 — API Hardening

- [ ] Request validation middleware (required fields, value ranges)
- [ ] Authentication / authorisation (API keys, HMAC-SHA256 webhook signatures)
- [ ] Rate limiting (per-merchant, per-IP)
- [ ] Structured logging (zerolog / zap) with trace IDs
- [ ] Distributed tracing (OpenTelemetry)
- [ ] Prometheus metrics (payment success rate, refund latency, p99 latency)
- [ ] Graceful shutdown

---

## Phase 3 — Resilience

- [ ] Outbox pattern for guaranteed event publishing to Kafka/SQS
  - Solves the dual-write problem: DB commit + event publish in one atomic step
- [ ] Dead-letter queue (DLQ) for webhooks that fail after N retries
- [ ] Circuit breaker for downstream payment-gateway calls
- [ ] Saga/compensation logic for distributed refund flows

---

## Phase 4 — Scalability

- [ ] Read replicas for account statement queries
- [ ] Partition `journal_entries` by `account_id` range (hot-account sharding)
- [ ] Async capture via work queue (Temporal / Asynq)
- [ ] Multi-currency support and FX rate service integration

---

## Phase 5 — Compliance & Audit

- [ ] Immutable audit log (append-only `journal_entries`, no UPDATE/DELETE)
- [ ] GDPR / data-retention policies for PII in payment records
- [ ] Reconciliation job (nightly balance cross-check between ledger and bank)
- [ ] SOC 2 / PCI-DSS controls checklist

---

## Phase 6 — AI / ML Features *(AI as primary)*

| Feature | Description |
|---------|-------------|
| **Fraud scoring** | Real-time ML model scores each payment on velocity, device fingerprint, and geo-anomaly signals |
| **Smart retry** | Reinforcement-learning agent learns optimal retry cadence for failed webhooks |
| **Anomaly detection** | Autoencoder flags unusual ledger patterns (sudden large debits, balance spikes) |
| **LLM-assisted reconciliation** | GPT-4o reads bank statements and auto-matches entries, flagging discrepancies |
| **AI chatbot (ledger assistant)** | Internal tool: engineers query "show me all failed refunds for merchant X last week" in natural language |
| **Predictive dispute resolution** | Model predicts chargeback likelihood and pre-emptively flags high-risk transactions |

---

## Milestone Timeline (indicative)

```
Q1 2026  Phase 1  Persistence
Q2 2026  Phase 2  API Hardening
Q3 2026  Phase 3  Resilience
Q3 2026  Phase 4  Scalability
Q4 2026  Phase 5  Compliance
2027     Phase 6  AI features
```
