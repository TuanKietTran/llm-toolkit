// Command mockats serves the local mock ATS (Greenhouse-shaped board + authenticated
// applications + lookup) for end-to-end tests and demos. Never point it at real data.
//
//	MOCKATS_ADDR  (default 127.0.0.1:8791)
//	MOCKATS_BOARD (default demo)
//	MOCKATS_KEY   API key the candidate account will hold (required)
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/mockats"
)

func main() {
	key := os.Getenv("MOCKATS_KEY")
	if key == "" {
		log.Fatal("MOCKATS_KEY is required")
	}
	addr := os.Getenv("MOCKATS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8791"
	}
	board := os.Getenv("MOCKATS_BOARD")
	if board == "" {
		board = "demo"
	}
	srv := mockats.New(board, "Demo Payments (mock)", key, mockats.DemoJobs())
	mux := http.NewServeMux()
	mux.Handle("/v1/", srv)
	mux.HandleFunc("GET /_mock/applications", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeApps(w, srv)
	})
	log.Printf("mock ATS on http://%s/v1/boards/%s/jobs", addr, board)
	hs := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(hs.ListenAndServe())
}
