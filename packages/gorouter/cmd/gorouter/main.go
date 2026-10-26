package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gorouter/internal/config"
	"gorouter/internal/oauth"
	"gorouter/internal/server"
	"gorouter/internal/store"
	"gorouter/internal/usage"
)

func main() {
	cfgPath := flag.String("config", "config.json", "seed config file (used when the database has no config yet)")
	dbPath := flag.String("db", "data/gorouter.db", "SQLite database path")
	reseed := flag.Bool("reseed", false, "overwrite the stored config with -config")
	flag.Parse()

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	cfg, err := loadConfig(st, *cfgPath, *reseed)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	tracker := usage.New(st)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { tracker.Run(stop); close(done) }()

	api := server.New(server.Options{Config: cfg, Store: st, OAuth: oauth.NewManager(st), Usage: tracker, Quotas: st})
	quotaDone := make(chan struct{})
	go func() { api.Run(stop); close(quotaDone) }()
	handler := api.Handler()
	srv := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("gorouter listening on %s (db %s, dashboard /dashboard/)", cfg.Listen, *dbPath)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	close(stop)
	<-done
	<-quotaDone
}

// loadConfig prefers the database copy; the file seeds it on first run.
func loadConfig(st *store.Store, path string, reseed bool) (*config.Config, error) {
	if !reseed {
		raw, err := st.RawConfig()
		if err != nil {
			return nil, err
		}
		if raw != nil {
			return config.Parse(raw)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		return nil, err
	}
	return cfg, st.SaveRawConfig(raw)
}
