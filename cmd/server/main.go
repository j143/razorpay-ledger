// Command server starts the Razorpay ledger HTTP service.
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/j143/razorpay-ledger/api"
	"github.com/j143/razorpay-ledger/internal/ledger"
	"github.com/j143/razorpay-ledger/internal/payment"
	"github.com/j143/razorpay-ledger/internal/refund"
	"github.com/j143/razorpay-ledger/internal/webhook"
	"github.com/j143/razorpay-ledger/pkg/models"
	"github.com/j143/razorpay-ledger/pkg/store"
)

func main() {
	// In-memory store (replace with *store.PostgresStore in production).
	s := store.New()

	// Wire up services.
	ledgerSvc := ledger.New(s)
	paymentSvc := payment.New(s, ledgerSvc)
	refundSvc := refund.New(s, ledgerSvc)
	webhookProc := webhook.New(s)

	// Register built-in webhook handlers.
	webhookProc.Register("payment.captured", func(e *models.WebhookEvent) error {
		log.Printf("[webhook] payment.captured event_id=%s", e.EventID)
		return nil
	})

	h := api.NewHandler(ledgerSvc, paymentSvc, refundSvc, webhookProc)
	mux := http.NewServeMux()
	h.Routes(mux)

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("razorpay-ledger listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
