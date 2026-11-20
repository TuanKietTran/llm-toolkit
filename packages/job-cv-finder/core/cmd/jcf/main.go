// Command jcf runs the Job CV Finder core: HTTP API, workflow engine and scheduler in one
// process (a modular monolith; Python intelligence workers run separately).
//
// Configuration (environment):
//
//	JCF_ADDR              listen address (default 127.0.0.1:8790)
//	JCF_DB                SQLite path (default ./data/jcf.db)
//	JCF_API_TOKEN         bearer token for /api/v1 (required; held by Ruxt's server proxy)
//	JCF_WORKER_TOKEN      bearer token for /internal/v1 (required; held by Python workers)
//	JCF_SECRET_KEY        base64 32-byte key for credentials at rest (optional; without it
//	                      credential storage is refused)
//	JCF_GREENHOUSE_BOARDS comma list of token:Company[:location regex] for public Greenhouse boards
//	JCF_LEVER_SITES       comma list of site:Company for public Lever postings
//	JCF_MOCKATS_URL       enable the local mock ATS source (tests/demo only)
//	JCF_MOCKATS_BOARD     its board token (default demo)
//	JCF_DISCOVER_EVERY    discovery interval for polling sources (default 6h)
//	JCF_LOG_LEVEL         debug|info|warn (default info)
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/api"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/domain"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/execution"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/logging"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/secrets"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/sources"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/store"
	"github.com/TuanKietTran/llm-toolkit/packages/job-cv-finder/core/internal/workflow"
)

const version = "0.1.0"

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func main() {
	level := map[string]slog.Level{"debug": slog.LevelDebug, "warn": slog.LevelWarn}[env("JCF_LOG_LEVEL", "info")]
	log := logging.New(os.Stdout, level)
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	apiToken, workerToken := os.Getenv("JCF_API_TOKEN"), os.Getenv("JCF_WORKER_TOKEN")
	if len(apiToken) < 24 || len(workerToken) < 24 {
		return errors.New("JCF_API_TOKEN and JCF_WORKER_TOKEN must be set (at least 24 characters each)")
	}
	if apiToken == workerToken {
		return errors.New("JCF_API_TOKEN and JCF_WORKER_TOKEN must differ")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dbPath := env("JCF_DB", "./data/jcf.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return err
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	vault, err := secrets.New(st.DB, os.Getenv("JCF_SECRET_KEY"))
	if err != nil {
		return err
	}
	if !vault.Configured() {
		log.Warn("JCF_SECRET_KEY not set: credential storage disabled; automated API submission unavailable")
	}

	for _, s := range sources.Defaults() {
		if err := st.SeedSource(ctx, s); err != nil {
			return err
		}
	}
	for _, pair := range splitList(os.Getenv("JCF_GREENHOUSE_BOARDS")) {
		tok, rest, _ := strings.Cut(pair, ":")
		company, filter, _ := strings.Cut(rest, ":")
		src := sources.GreenhouseBoard(tok, company)
		src.Config["company"] = company
		if filter != "" {
			src.Config["location_filter"] = filter
		}
		if err := st.SeedSource(ctx, src); err != nil {
			return err
		}
	}
	for _, pair := range splitList(os.Getenv("JCF_LEVER_SITES")) {
		site, company, _ := strings.Cut(pair, ":")
		if err := st.SeedSource(ctx, sources.LeverSite(site, company)); err != nil {
			return err
		}
	}
	if u := os.Getenv("JCF_MOCKATS_URL"); u != "" {
		src := sources.MockATS(u, env("JCF_MOCKATS_BOARD", "demo"))
		src.Config["company"] = "Demo Payments (mock)"
		if err := st.SeedSource(ctx, src); err != nil {
			return err
		}
		log.Warn("mock ATS source enabled; for local testing only", "url", u)
	}

	httpClient := &http.Client{Timeout: 45 * time.Second}
	execs := map[domain.AccessMethod]execution.Executor{
		domain.MethodAuthorizedAPI: &execution.AuthorizedAPIExecutor{Client: httpClient, Secrets: vault, Accounts: st},
	}
	every, err := time.ParseDuration(env("JCF_DISCOVER_EVERY", "6h"))
	if err != nil {
		return err
	}
	eng := workflow.New(st, sources.NewRegistry(httpClient), execs, workflow.Config{DiscoverEvery: every}, log)
	srv := &api.Server{Engine: eng, Vault: vault, APIToken: apiToken, WorkerToken: workerToken, Log: log, Version: version}

	go eng.Run(ctx)
	hs := &http.Server{Addr: env("JCF_ADDR", "127.0.0.1:8790"), Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Info("job-cv-finder core listening", "addr", hs.Addr, "db", dbPath, "version", version)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
