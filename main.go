// Command ticketd is a lightweight, self-contained IT ticketing system.
package main

import (
	"bufio"
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ticketd/internal/config"
	"ticketd/internal/store"
	"ticketd/internal/web"
)

//go:embed all:templates
var templatesFS embed.FS

//go:embed all:static
var staticFS embed.FS

func main() {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		os.Exit(2)
	}

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("ticketd: %v", err)
	}
	defer st.DB.Close()

	if err := bootstrapAdmin(st, cfg); err != nil {
		log.Fatalf("ticketd: %v", err)
	}

	tmpl, err := fs.Sub(templatesFS, "templates")
	if err != nil {
		log.Fatalf("templates: %v", err)
	}
	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("static: %v", err)
	}
	app, err := web.New(cfg, st, tmpl, static)
	if err != nil {
		log.Fatalf("ticketd: %v", err)
	}

	addr := fmt.Sprintf("%s:%d", cfg.BindHost, cfg.BindPort)
	srv := &http.Server{
		Addr:              addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	sessDone := make(chan struct{})
	go cleanupSessions(st, sessDone)

	errCh := make(chan error, 1)
	go func() {
		log.Printf("ticketd listening on http://%s (data dir: %s)", addr, cfg.DataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-stop:
		log.Printf("shutting down")
	case err := <-errCh:
		log.Fatalf("ticketd: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	close(sessDone)
}

// bootstrapAdmin ensures at least one admin account exists.
func bootstrapAdmin(st *store.Store, cfg *config.Config) error {
	has, err := st.HasAnyAdmin()
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	username, password := cfg.AdminUsername, cfg.AdminPassword
	if username == "" || password == "" {
		if !stdinIsTerminal() {
			return errors.New("no admin account exists; set ADMIN_USERNAME and ADMIN_PASSWORD (or run in a terminal) to create the first one")
		}
		fmt.Print("No accounts yet — creating the first administrator.\nUsername: ")
		sc := bufio.NewScanner(os.Stdin)
		if !sc.Scan() {
			return errors.New("no input while prompting for admin username")
		}
		username = sc.Text()
		fmt.Print("Password: ")
		if !sc.Scan() {
			return errors.New("no input while prompting for admin password")
		}
		password = sc.Text()
	}
	if _, err := st.CreateUser(username, username, password, "admin"); err != nil {
		return fmt.Errorf("create initial admin: %w", err)
	}
	log.Printf("created initial admin account %q", username)
	return nil
}

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func cleanupSessions(st *store.Store, done <-chan struct{}) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	st.DeleteExpiredSessions()
	for {
		select {
		case <-t.C:
			st.DeleteExpiredSessions()
		case <-done:
			return
		}
	}
}
