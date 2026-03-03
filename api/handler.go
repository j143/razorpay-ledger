// Package api wires together the HTTP handlers for the Razorpay ledger service.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/j143/razorpay-ledger/internal/ledger"
	"github.com/j143/razorpay-ledger/internal/payment"
	"github.com/j143/razorpay-ledger/internal/refund"
	"github.com/j143/razorpay-ledger/internal/webhook"
	"github.com/j143/razorpay-ledger/pkg/models"
	"github.com/j143/razorpay-ledger/pkg/store"
)

// Handler bundles all service dependencies and returns an http.ServeMux.
type Handler struct {
	ledger  *ledger.Service
	payment *payment.Service
	refund  *refund.Service
	webhook *webhook.Processor
}

// NewHandler wires up all services and returns a configured Handler.
func NewHandler(
	l *ledger.Service,
	p *payment.Service,
	r *refund.Service,
	w *webhook.Processor,
) *Handler {
	return &Handler{ledger: l, payment: p, refund: r, webhook: w}
}

// Routes registers all HTTP routes on the provided mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	// Ledger
	mux.HandleFunc("POST /v1/accounts", h.createAccount)
	mux.HandleFunc("GET /v1/accounts/{id}", h.getAccount)
	mux.HandleFunc("GET /v1/accounts/{id}/statement", h.getStatement)

	// Payments
	mux.HandleFunc("POST /v1/payments", h.createPayment)
	mux.HandleFunc("POST /v1/payments/{id}/capture", h.capturePayment)

	// Refunds
	mux.HandleFunc("POST /v1/refunds", h.createRefund)

	// Webhooks
	mux.HandleFunc("POST /v1/webhooks/events", h.ingestWebhook)
}

// ------------------------------------------------------------------ helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// ------------------------------------------------------------------ Ledger handlers

func (h *Handler) createAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID       string         `json:"id"`
		Name     string         `json:"name"`
		Currency models.Currency `json:"currency"`
		Balance  int64          `json:"balance"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a := &models.Account{
		ID:       req.ID,
		Name:     req.Name,
		Currency: req.Currency,
		Balance:  req.Balance,
	}
	if err := h.ledger.CreateAccount(a); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (h *Handler) getAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, err := h.ledger.GetAccount(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (h *Handler) getStatement(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entries, err := h.ledger.GetAccountStatement(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// ------------------------------------------------------------------ Payment handlers

func (h *Handler) createPayment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID             string          `json:"id"`
		IdempotencyKey string          `json:"idempotency_key"`
		Amount         int64           `json:"amount"`
		Currency       models.Currency `json:"currency"`
		CustomerID     string          `json:"customer_id"`
		MerchantID     string          `json:"merchant_id"`
		Description    string          `json:"description"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := &models.Payment{
		ID:             req.ID,
		IdempotencyKey: req.IdempotencyKey,
		Amount:         req.Amount,
		Currency:       req.Currency,
		CustomerID:     req.CustomerID,
		MerchantID:     req.MerchantID,
		Description:    req.Description,
	}
	result, err := h.payment.CreateOrGetPayment(p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *Handler) capturePayment(w http.ResponseWriter, r *http.Request) {
	paymentID := r.PathValue("id")
	var req struct {
		TxnID             string `json:"txn_id"`
		CustomerAccountID string `json:"customer_account_id"`
		MerchantAccountID string `json:"merchant_account_id"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.payment.CapturePayment(paymentID, req.TxnID, req.CustomerAccountID, req.MerchantAccountID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ------------------------------------------------------------------ Refund handlers

func (h *Handler) createRefund(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID                string `json:"id"`
		IdempotencyKey    string `json:"idempotency_key"`
		PaymentID         string `json:"payment_id"`
		Amount            int64  `json:"amount"`
		Reason            string `json:"reason"`
		TxnID             string `json:"txn_id"`
		MerchantAccountID string `json:"merchant_account_id"`
		CustomerAccountID string `json:"customer_account_id"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ref := &models.Refund{
		ID:             req.ID,
		IdempotencyKey: req.IdempotencyKey,
		PaymentID:      req.PaymentID,
		Amount:         req.Amount,
		Reason:         req.Reason,
	}
	result, err := h.refund.CreateOrGetRefund(ref, req.TxnID, req.MerchantAccountID, req.CustomerAccountID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

// ------------------------------------------------------------------ Webhook handler

func (h *Handler) ingestWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		Payload   string `json:"payload"`
		Source    string `json:"source"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	event := &models.WebhookEvent{
		EventID:    req.EventID,
		EventType:  req.EventType,
		Payload:    req.Payload,
		Source:     req.Source,
		ReceivedAt: time.Now().UTC(),
	}
	result, err := h.webhook.Process(event)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	status := http.StatusOK
	if !result.Duplicate {
		status = http.StatusCreated
	}
	resp := map[string]any{
		"duplicate": result.Duplicate,
		"event":     result.Event,
	}
	writeJSON(w, status, resp)
}
