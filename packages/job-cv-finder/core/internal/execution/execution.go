// Package execution submits applications through adapters chosen by the policy engine:
//
//	ApplicationExecutor
//	  ├── AuthorizedAPIExecutor   (ATS API with a key the candidate legitimately holds)
//	  ├── PermittedBrowserExecutor (not registered: no source currently permits it)
//	  └── ExternalHandoffExecutor (candidate submits on the platform; email receipts verify)
//
// Executors never decide whether to submit; the workflow + policy engine do. Executors
// classify failures so the workflow can tell "safe to retry" from "outcome unknown".
package execution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
)

// Contact is the minimum identity an application form needs. It comes from the Ruxt
// profile's own contacts; nothing is invented.
type Contact struct {
	FullName string `json:"full_name"`
	Email    string `json:"email"`
	Phone    string `json:"phone,omitempty"`
}

// SubmitRequest carries everything an executor may send.
type SubmitRequest struct {
	Application    store.Application
	Job            domain.Job
	Source         store.Source
	Contact        Contact
	Documents      []store.Document
	Answers        []store.Answer
	IdempotencyKey string
}

// SubmitResult is what the external system acknowledged.
type SubmitResult struct {
	ExternalRef string          `json:"external_ref"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// LookupResult is the answer to "does the external system have this application?".
type LookupResult struct {
	Found       bool            `json:"found"`
	ExternalRef string          `json:"external_ref,omitempty"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

// Executor submits and verifies applications for sources whose policy permits it.
type Executor interface {
	Name() string
	Submit(ctx context.Context, req SubmitRequest) (SubmitResult, error)
	// Lookup checks the external system by idempotency key. Authoritative reports whether a
	// "not found" answer is reliable enough to justify a retry.
	Lookup(ctx context.Context, src store.Source, idempotencyKey string) (LookupResult, error)
	Authoritative() bool
}

// Failure classes.
var (
	// ErrRetryable: the request certainly did not take effect (e.g. connection refused, 5xx
	// before acceptance, 429). Safe to retry after backoff.
	ErrRetryable = errors.New("retryable")
	// ErrUncertain: the request may have taken effect (timeout after sending). Reconcile first.
	ErrUncertain = errors.New("outcome uncertain")
	// ErrPermanent: rejected (4xx validation). Retrying the same request cannot succeed.
	ErrPermanent = errors.New("permanent")
	// ErrAuth: credentials missing/invalid; the account needs re-authentication.
	ErrAuth = errors.New("authentication required")
)

// Class wraps err with a failure class.
type Class struct {
	Kind error
	Err  error
}

func (c *Class) Error() string   { return c.Kind.Error() + ": " + c.Err.Error() }
func (c *Class) Unwrap() []error { return []error{c.Kind, c.Err} }

func classify(kind, err error) error { return &Class{Kind: kind, Err: err} }

// HandoffName is the pseudo-executor recorded for candidate-performed submissions.
const HandoffName = "handoff"
