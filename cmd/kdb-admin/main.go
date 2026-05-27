// kdb-admin — standalone HTTP server for the KDB administrative UI.
//
// Runs as a separate binary/container from kdb-api so that admin changes
// cannot break the lookup API. Shares the same Postgres pool and database
// as kdb-api/kdb-worker.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"github.com/rickyjoo73/kdb/internal/db"
	"github.com/rickyjoo73/kdb/internal/kdbadmin"
)

func main() {
	_ = godotenv.Load()
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC | log.Lshortfile)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.New(ctx)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	port := os.Getenv("KDB_ADMIN_PORT")
	if port == "" {
		port = "9101"
	}

	var secret []byte
	if s := os.Getenv("KDB_ADMIN_SESSION_SECRET"); s != "" {
		secret = []byte(s)
	}

	handler := kdbadmin.NewRouter(pool, kdbadmin.Options{
		SessionSecret: secret,
		LogRequests:   true,
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	log.Printf("kdb-admin listening on :%s", port)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("listen: %v", err)
	}
}
