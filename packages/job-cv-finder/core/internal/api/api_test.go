package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/execution"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/logging"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/mockats"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/secrets"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/sources"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/workflow"
)

const apiTok, wrkTok = "api-token-0123456789abcdef", "worker-token-0123456789abcd"

type env struct {
	t   *testing.T
	srv *httptest.Server
	ats *httptest.Server
	log *bytes.Buffer
}

func newEnv(t *testing.T) *env {
	ctx := context.Background()
	st, err := store.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, 32)
	rand.Read(key)
	vault, _ := secrets.New(st.DB, base64.StdEncoding.EncodeToString(key))
	ats := httptest.NewServer(mockats.New("demo", "Demo", "the-ats-key", mockats.DemoJobs()))
	t.Cleanup(ats.Close)
	for _, s := range sources.Defaults() {
		st.SeedSource(ctx, s)
	}
	st.SeedSource(ctx, sources.MockATS(ats.URL, "demo"))
	buf := &bytes.Buffer{}
	log := logging.New(buf, slog.LevelDebug)
	eng := workflow.New(st, sources.NewRegistry(ats.Client()), map[domain.AccessMethod]execution.Executor{
		domain.MethodAuthorizedAPI: &execution.AuthorizedAPIExecutor{Client: ats.Client(), Secrets: vault, Accounts: st}}, workflow.Config{}, log)
	s := &Server{Engine: eng, Vault: vault, APIToken: apiTok, WorkerToken: wrkTok, Log: log, Version: "test"}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, srv: srv, ats: ats, log: buf}
}

func (e *env) do(method, path, token, body string) (int, []byte) {
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestAuthSeparatesCandidateAndWorkerAPIs(t *testing.T) {
	e := newEnv(t)
	if c, _ := e.do("GET", "/api/v1/overview", "", ""); c != 401 {
		t.Fatalf("anonymous: %d", c)
	}
	if c, _ := e.do("GET", "/api/v1/overview", wrkTok, ""); c != 401 {
		t.Fatalf("worker token on candidate API: %d", c)
	}
	if c, _ := e.do("POST", "/internal/v1/tasks/lease", apiTok, `{"kinds":["intel.match"],"worker":"w"}`); c != 401 {
		t.Fatalf("api token on worker API: %d", c)
	}
	if c, b := e.do("GET", "/api/v1/overview", apiTok, ""); c != 200 {
		t.Fatalf("overview %d %s", c, b)
	}
}

func TestCredentialIsWriteOnlyAndRefusedForPlatforms(t *testing.T) {
	e := newEnv(t)
	if c, b := e.do("PUT", "/api/v1/accounts/linkedin/credential", apiTok, `{"credential":"my-linkedin-password"}`); c != 400 || !strings.Contains(string(b), "does not store platform passwords") {
		t.Fatalf("linkedin credential: %d %s", c, b)
	}
	if c, b := e.do("PUT", "/api/v1/accounts/mockats:demo/credential", apiTok, `{"credential":"the-ats-key"}`); c != 200 {
		t.Fatalf("mock credential: %d %s", c, b)
	}
	_, b := e.do("GET", "/api/v1/accounts", apiTok, "")
	if strings.Contains(string(b), "the-ats-key") || strings.Contains(string(b), "secret_ref") || !strings.Contains(string(b), `"has_secret":true`) {
		t.Fatalf("accounts leaked or wrong: %s", b)
	}
	_, ov := e.do("GET", "/api/v1/overview", apiTok, "")
	if strings.Contains(string(ov), "the-ats-key") || strings.Contains(e.log.String(), "the-ats-key") {
		t.Fatal("credential leaked into overview or logs")
	}
}

func TestWorkerProtocolOverHTTPAndSSE(t *testing.T) {
	e := newEnv(t)
	snap := `{"schema_version":1,"source":{"system":"ruxt","exported_at":"2026-11-20T00:00:00Z"},
	 "profile":{"profile_id":"p","updated_at":"2026-11-19T00:00:00Z","data":{"identity":{"fullName":"A B"},"contacts":[]}},
	 "cvs":[{"cv_id":"cv1","revision":1,"title":"CV","markdown":"# A","updated_at":"2026-11-19T00:00:00Z"}],"preferences":{}}`
	if c, b := e.do("POST", "/api/v1/candidate/snapshots", apiTok, snap); c != 201 {
		t.Fatalf("snapshot %d %s", c, b)
	}
	if c, _ := e.do("POST", "/api/v1/candidate/snapshots", apiTok, snap); c != 200 {
		t.Fatalf("re-sent snapshot should be idempotent (200), got %d", c)
	}
	if c, b := e.do("POST", "/api/v1/candidate/snapshots", apiTok, `{"schema_version":1}`); c != 422 {
		t.Fatalf("invalid snapshot %d %s", c, b)
	}
	if c, b := e.do("POST", "/api/v1/sources/mockats:demo/discover", apiTok, ""); c != 200 {
		t.Fatalf("discover %d %s", c, b)
	}
	if c, b := e.do("POST", "/api/v1/sources/linkedin/discover", apiTok, ""); c != 422 {
		t.Fatalf("polling linkedin must be refused, got %d %s", c, b)
	}
	c, b := e.do("POST", "/internal/v1/tasks/lease", wrkTok, `{"kinds":["intel.match"],"worker":"w1","lease_seconds":60}`)
	if c != 200 {
		t.Fatalf("lease %d %s", c, b)
	}
	var task struct{ ID string }
	json.Unmarshal(b, &task)
	if c, b := e.do("POST", "/internal/v1/tasks/"+task.ID+"/complete", wrkTok, `{"worker":"w1","result":{"job_id":"x"}}`); c != 422 {
		t.Fatalf("contract violation must be 422, got %d %s", c, b)
	}
	if c, _ := e.do("POST", "/internal/v1/tasks/"+task.ID+"/complete", wrkTok, `{"worker":"w1","result":{}}`); c != 409 {
		t.Fatalf("completion after failure must be lease-lost 409, got %d", c)
	}

	// SSE: replay from the start and see the discovery event.
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/events/stream?after=0", nil)
	req.Header.Set("Authorization", "Bearer "+apiTok)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	found := false
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "event: discovery.finished") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("SSE did not deliver discovery.finished")
	}
}

func TestManualLinkedInURLUsesEmailJobID(t *testing.T) {
	if id := externalIDFromURL("linkedin", "https://www.linkedin.com/jobs/view/4486004372/?trk=x"); id != "4486004372" {
		t.Fatalf("got %q", id)
	}
	e := newEnv(t)
	c, b := e.do("POST", "/api/v1/jobs/manual", apiTok, `{"source":"linkedin","url":"https://www.linkedin.com/jobs/view/4486004372/","company":"Acme","title":"Go Engineer","description":"Golang, Docker"}`)
	if c != 201 {
		t.Fatalf("%d %s", c, b)
	}
}

func TestRedaction(t *testing.T) {
	for _, k := range []string{"password", "api_key", "Authorization", "session_cookie", "credential"} {
		if !logging.Redact(k) {
			t.Errorf("%s not redacted", k)
		}
	}
}
