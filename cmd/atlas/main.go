package main

import (
	"atlas/internal/app"
	"atlas/internal/gravitas"
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	var auth *gravitas.Client
	origin := os.Getenv("APP_URL")
	if origin == "" {
		origin = "http://localhost:8080"
	}
	if key := os.Getenv("GRAVITAS_SESSION_KEY"); key != "" {
		var err error
		auth, err = gravitas.New("https://gravitas.cool", origin, key)
		if err != nil {
			log.Fatal(err)
		}
	}
	path := os.Getenv("DB_PATH")
	if path == "" {
		path = "atlas.db"
	}
	s, err := app.New(path, origin, auth)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	server := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("Atlas listening on %s", addr)
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
