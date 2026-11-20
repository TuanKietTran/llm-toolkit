// Package mockats is a local ATS used for integration and end-to-end tests. It serves a
// Greenhouse-Job-Board-shaped public board, accepts authenticated applications with
// idempotency, exposes a lookup endpoint, and can inject faults (5xx after storing, slow
// responses) so SUBMISSION_UNKNOWN reconciliation is exercised for real.
package mockats

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Job is a board posting.
type Job struct {
	ID        int64      `json:"id"`
	Title     string     `json:"title"`
	Location  string     `json:"location"`
	Content   string     `json:"content"`
	Questions []Question `json:"questions"`
}

// Question is a form question.
type Question struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

// Application is a stored submission.
type Application struct {
	ID             string            `json:"id"`
	JobID          string            `json:"job_id"`
	IdempotencyKey string            `json:"idempotency_key"`
	Fields         map[string]string `json:"fields"`
	Files          map[string]int    `json:"files"` // field -> size
	ReceivedAt     time.Time         `json:"received_at"`
}

// Faults inject failures into the NEXT n submissions.
type Faults struct {
	FailAfterStore atomic.Int32 // store the application, then answer 503
	FailBefore     atomic.Int32 // answer 503 without storing
	Delay          atomic.Int64 // nanoseconds to sleep before answering
}

// Server is the mock ATS.
type Server struct {
	Board   string
	Company string
	APIKey  string
	Faults  Faults

	mu   sync.Mutex
	jobs []Job
	apps map[string]*Application // by idempotency key
	seq  int
}

// New creates a mock ATS with a board and key.
func New(board, company, apiKey string, jobs []Job) *Server {
	return &Server{Board: board, Company: company, APIKey: apiKey, jobs: jobs, apps: map[string]*Application{}}
}

// Applications returns stored applications (test assertions).
func (s *Server) Applications() []Application {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Application, 0, len(s.apps))
	for _, a := range s.apps {
		out = append(out, *a)
	}
	return out
}

func (s *Server) authorized(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(s.APIKey+":"))
	return subtle.ConstantTimeCompare([]byte(h), []byte(want)) == 1
}

// ServeHTTP routes: GET /v1/boards/{b}/jobs, GET /v1/boards/{b}/jobs/{id},
// POST /v1/boards/{b}/jobs/{id}, GET /v1/boards/{b}/applications?idempotency_key=.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	prefix := "/v1/boards/" + s.Board + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		http.NotFound(w, r)
		return
	}
	rest := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && len(rest) == 1 && rest[0] == "jobs":
		s.listJobs(w)
	case r.Method == http.MethodGet && len(rest) == 2 && rest[0] == "jobs":
		s.getJob(w, rest[1])
	case r.Method == http.MethodPost && len(rest) == 2 && rest[0] == "jobs":
		s.apply(w, r, rest[1])
	case r.Method == http.MethodGet && len(rest) == 1 && rest[0] == "applications":
		s.lookup(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) ghJob(j Job) map[string]any {
	qs := []map[string]any{}
	for _, q := range j.Questions {
		qs = append(qs, map[string]any{"label": q.Label, "required": q.Required, "fields": []map[string]string{{"name": q.Name, "type": q.Type}}})
	}
	return map[string]any{"id": j.ID, "title": j.Title, "absolute_url": fmt.Sprintf("http://mockats.test/%s/jobs/%d", s.Board, j.ID),
		"updated_at": "2026-11-15T00:00:00Z", "location": map[string]string{"name": j.Location}, "content": j.Content,
		"company_name": s.Company, "questions": qs}
}

func (s *Server) listJobs(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []map[string]any{}
	for _, j := range s.jobs {
		out = append(out, s.ghJob(j))
	}
	json.NewEncoder(w).Encode(map[string]any{"jobs": out})
}

func (s *Server) getJob(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if fmt.Sprint(j.ID) == id {
			json.NewEncoder(w).Encode(s.ghJob(j))
			return
		}
	}
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(`{"error":"not found"}`))
}

func (s *Server) apply(w http.ResponseWriter, r *http.Request, jobID string) {
	if d := s.Faults.Delay.Load(); d > 0 {
		time.Sleep(time.Duration(d))
	}
	if !s.authorized(r) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
		return
	}
	if dec(&s.Faults.FailBefore) {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if idem == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Idempotency-Key required"}`))
		return
	}
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error":%q}`, err.Error())
		return
	}
	s.mu.Lock()
	var job *Job
	for i := range s.jobs {
		if fmt.Sprint(s.jobs[i].ID) == jobID {
			job = &s.jobs[i]
		}
	}
	if job == nil {
		s.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if existing, ok := s.apps[idem]; ok { // replay: same answer, no duplicate
		s.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"id": existing.ID, "status": "received"})
		return
	}
	fields := map[string]string{}
	for k, v := range r.MultipartForm.Value {
		fields[k] = v[0]
	}
	files := map[string]int{}
	for k, fh := range r.MultipartForm.File {
		files[k] = int(fh[0].Size)
	}
	var missing []string
	for _, base := range []string{"first_name", "last_name", "email"} {
		if fields[base] == "" {
			missing = append(missing, base)
		}
	}
	for _, q := range job.Questions {
		if !q.Required {
			continue
		}
		if q.Type == "input_file" {
			if files[q.Name] == 0 {
				missing = append(missing, q.Name)
			}
		} else if fields[q.Name] == "" {
			missing = append(missing, q.Name)
		}
	}
	if len(missing) > 0 {
		s.mu.Unlock()
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]any{"error": "missing required fields", "fields": missing})
		return
	}
	s.seq++
	app := &Application{ID: fmt.Sprintf("mock-app-%d", s.seq), JobID: jobID, IdempotencyKey: idem, Fields: fields, Files: files, ReceivedAt: time.Now().UTC()}
	s.apps[idem] = app
	s.mu.Unlock()
	if dec(&s.Faults.FailAfterStore) {
		w.WriteHeader(http.StatusServiceUnavailable) // stored, but the client cannot know
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": app.ID, "status": "received"})
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	app, ok := s.apps[r.URL.Query().Get("idempotency_key")]
	s.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"not found"}`))
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"id": app.ID, "job_id": app.JobID, "received_at": app.ReceivedAt})
}

func dec(n *atomic.Int32) bool {
	for {
		v := n.Load()
		if v <= 0 {
			return false
		}
		if n.CompareAndSwap(v, v-1) {
			return true
		}
	}
}

// DemoJobs is the fixture board used by tests and the local demo.
func DemoJobs() []Job {
	return []Job{
		{ID: 1001, Title: "Senior Backend Engineer (Go)", Location: "Ho Chi Minh City (Hybrid)",
			Content: "&lt;p&gt;We build payment APIs.&lt;/p&gt;&lt;h3&gt;Requirements&lt;/h3&gt;&lt;ul&gt;&lt;li&gt;4+ years building backend services in Golang&lt;/li&gt;&lt;li&gt;PostgreSQL and Docker&lt;/li&gt;&lt;/ul&gt;&lt;h3&gt;Nice to have&lt;/h3&gt;&lt;ul&gt;&lt;li&gt;Kubernetes&lt;/li&gt;&lt;/ul&gt;",
			Questions: []Question{
				{Name: "resume", Label: "Resume/CV", Type: "input_file", Required: true},
				{Name: "cover_letter", Label: "Cover Letter", Type: "input_file", Required: false},
				{Name: "question_notice", Label: "What is your notice period?", Type: "input_text", Required: true},
			}},
		{ID: 1002, Title: "iOS Engineer (Swift)", Location: "Hanoi (Onsite)",
			Content:   "&lt;h3&gt;Requirements&lt;/h3&gt;&lt;ul&gt;&lt;li&gt;5+ years of Swift and UIKit&lt;/li&gt;&lt;/ul&gt;",
			Questions: []Question{{Name: "resume", Label: "Resume/CV", Type: "input_file", Required: true}}},
		{ID: 1003, Title: "Platform Engineer", Location: "Ho Chi Minh City (Hybrid)",
			Content: "&lt;h3&gt;Requirements&lt;/h3&gt;&lt;ul&gt;&lt;li&gt;3+ years with Docker and Kubernetes&lt;/li&gt;&lt;li&gt;Golang or Python&lt;/li&gt;&lt;/ul&gt;",
			Questions: []Question{
				{Name: "resume", Label: "Resume/CV", Type: "input_file", Required: true},
				{Name: "question_visa", Label: "Do you require visa sponsorship to work in Vietnam?", Type: "input_text", Required: true},
			}},
	}
}
