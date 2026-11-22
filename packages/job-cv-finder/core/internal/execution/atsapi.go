package execution

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
)

// SecretResolver returns a secret by handle. Implemented by the vault; the executor never
// stores or logs the value.
type SecretResolver interface {
	Get(ctx context.Context, ref string) ([]byte, error)
}

// AccountLookup finds the platform account for a source.
type AccountLookup interface {
	GetAccountBySource(ctx context.Context, sourceID string) (store.Account, error)
}

// AuthorizedAPIExecutor posts a Greenhouse-Job-Board-shaped multipart application with
// HTTP Basic auth (key as username). Real Greenhouse/Lever keys belong to employers, so
// policy only routes sources here whose submit capability is explicitly authorized_api
// (today: the mock ATS). The lookup endpoint is a mock-ATS extension: real Job Board APIs
// offer no candidate-side lookup, which is why their submit capability stays disabled.
type AuthorizedAPIExecutor struct {
	Client   *http.Client
	Secrets  SecretResolver
	Accounts AccountLookup
}

// Name implements Executor.
func (a *AuthorizedAPIExecutor) Name() string { return "authorized_api" }

// Authoritative implements Executor: the lookup reads the system of record.
func (a *AuthorizedAPIExecutor) Authoritative() bool { return true }

func (a *AuthorizedAPIExecutor) key(ctx context.Context, src store.Source) (string, error) {
	acct, err := a.Accounts.GetAccountBySource(ctx, src.ID)
	if err != nil || acct.SecretRef == nil {
		return "", classify(ErrAuth, fmt.Errorf("no connected account with a credential for %s", src.ID))
	}
	if acct.Status == "reauth_required" {
		return "", classify(ErrAuth, fmt.Errorf("account for %s requires re-authentication", src.ID))
	}
	k, err := a.Secrets.Get(ctx, *acct.SecretRef)
	if err != nil {
		return "", classify(ErrAuth, err)
	}
	return string(k), nil
}

func (a *AuthorizedAPIExecutor) endpoint(src store.Source, parts ...string) string {
	base := strings.TrimRight(src.Config["base_url"], "/")
	return base + "/v1/boards/" + url.PathEscape(src.Config["board_token"]) + "/" + strings.Join(parts, "/")
}

// Submit implements Executor.
func (a *AuthorizedAPIExecutor) Submit(ctx context.Context, req SubmitRequest) (SubmitResult, error) {
	key, err := a.key(ctx, req.Source)
	if err != nil {
		return SubmitResult{}, err
	}
	if req.Contact.Email == "" || req.Contact.FullName == "" {
		return SubmitResult{}, classify(ErrPermanent, errors.New("candidate profile has no name/email contact"))
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	first, last := splitName(req.Contact.FullName)
	fields := map[string]string{"first_name": first, "last_name": last, "email": req.Contact.Email, "phone": req.Contact.Phone}
	for k, v := range fields {
		if v != "" {
			_ = mw.WriteField(k, v)
		}
	}
	for _, ans := range req.Answers {
		if ans.Status != "answered" || ans.Answer == nil {
			continue
		}
		// Identity fields were written above; the text twin of an attachment slot only records that
		// the file satisfies it, and its placeholder must never be sent as the resume text.
		if fields[ans.QuestionID] != "" || strings.HasSuffix(ans.QuestionID, "_text") && strings.HasPrefix(*ans.Answer, "see attached") {
			continue
		}
		_ = mw.WriteField(ans.QuestionID, *ans.Answer)
	}
	for _, d := range req.Documents {
		field := map[string]string{"tailored_cv": "resume", "cover_letter": "cover_letter"}[d.Kind]
		if field == "" {
			continue
		}
		fw, err := mw.CreateFormFile(field, d.Kind+".md")
		if err != nil {
			return SubmitResult{}, err
		}
		_, _ = fw.Write([]byte(d.Content))
	}
	_ = mw.Close()

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint(req.Source, "jobs", url.PathEscape(req.Job.ExternalJobID)), &body)
	if err != nil {
		return SubmitResult{}, err
	}
	hreq.Header.Set("Content-Type", mw.FormDataContentType())
	hreq.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(key+":")))
	hreq.Header.Set("Idempotency-Key", req.IdempotencyKey)
	resp, err := a.Client.Do(hreq)
	if err != nil {
		return SubmitResult{}, classifyTransport(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		var ack struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &ack); err != nil || ack.ID == "" {
			// Accepted but unreadable acknowledgement: the application may exist.
			return SubmitResult{Raw: safeRaw(raw)}, classify(ErrUncertain, fmt.Errorf("unreadable acknowledgement"))
		}
		return SubmitResult{ExternalRef: ack.ID, Raw: safeRaw(raw)}, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return SubmitResult{}, classify(ErrAuth, fmt.Errorf("HTTP %d", resp.StatusCode))
	case resp.StatusCode == http.StatusTooManyRequests:
		return SubmitResult{}, classify(ErrRetryable, fmt.Errorf("HTTP 429"))
	case resp.StatusCode >= 500:
		// A 5xx can arrive after the ATS stored the application; reconcile before retrying.
		return SubmitResult{}, classify(ErrUncertain, fmt.Errorf("HTTP %d", resp.StatusCode))
	default:
		return SubmitResult{}, classify(ErrPermanent, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300)))
	}
}

// Lookup implements Executor.
func (a *AuthorizedAPIExecutor) Lookup(ctx context.Context, src store.Source, idem string) (LookupResult, error) {
	key, err := a.key(ctx, src)
	if err != nil {
		return LookupResult{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint(src, "applications")+"?idempotency_key="+url.QueryEscape(idem), nil)
	if err != nil {
		return LookupResult{}, err
	}
	hreq.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(key+":")))
	resp, err := a.Client.Do(hreq)
	if err != nil {
		return LookupResult{}, classifyTransport(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
		var got struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &got); err != nil || got.ID == "" {
			return LookupResult{}, classify(ErrRetryable, fmt.Errorf("unreadable lookup response"))
		}
		return LookupResult{Found: true, ExternalRef: got.ID, Raw: safeRaw(raw)}, nil
	case http.StatusNotFound:
		return LookupResult{Found: false}, nil
	default:
		return LookupResult{}, classify(ErrRetryable, fmt.Errorf("lookup HTTP %d", resp.StatusCode))
	}
}

func classifyTransport(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) {
		return classify(ErrUncertain, err) // request may have been received
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return classify(ErrRetryable, err) // never reached the server
	}
	return classify(ErrUncertain, err)
}

func splitName(full string) (string, string) {
	parts := strings.Fields(full)
	if len(parts) <= 1 {
		return full, ""
	}
	return strings.Join(parts[:len(parts)-1], " "), parts[len(parts)-1]
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func safeRaw(b []byte) json.RawMessage {
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	q, _ := json.Marshal(truncate(string(b), 500))
	return q
}

// Timeout for one submission call; kept short so a hung ATS becomes SUBMISSION_UNKNOWN fast.
const SubmitTimeout = 30 * time.Second
