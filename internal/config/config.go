// Package config assembles runtime settings from CLI flags, environment
// variables, and defaults. Flags win over environment variables.
package config

import (
	"flag"
	"os"
	"strconv"
)

type Config struct {
	BindHost      string
	BindPort      int
	DataDir       string
	MaxUploadMB   int64
	TicketPrefix  string
	CookieSecure  bool
	AdminUsername string
	AdminPassword string
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// Load parses command-line flags whose defaults come from the environment.
func Load(args []string) (*Config, error) {
	c := &Config{}
	fs := flag.NewFlagSet("ticketd", flag.ContinueOnError)
	var bindPort int
	fs.StringVar(&c.BindHost, "bind-host", envStr("BIND_HOST", "0.0.0.0"),
		"address to listen on (use 0.0.0.0 to expose on the local network)")
	fs.IntVar(&bindPort, "bind-port", int(envInt("BIND_PORT", 8080)), "port to listen on")
	fs.StringVar(&c.DataDir, "data-dir", envStr("DATA_DIR", "./data"), "directory for the database and uploads")
	var maxUpload int64
	fs.Int64Var(&maxUpload, "max-upload-mb", envInt("MAX_UPLOAD_MB", 50), "maximum attachment size in MB")
	fs.StringVar(&c.TicketPrefix, "ticket-prefix", envStr("TICKET_PREFIX", ""), "display prefix for ticket numbers, e.g. IT-")
	fs.BoolVar(&c.CookieSecure, "cookie-secure", envBool("COOKIE_SECURE", false), "mark session cookie Secure (enable when serving over HTTPS)")
	fs.StringVar(&c.AdminUsername, "admin-username", os.Getenv("ADMIN_USERNAME"), "username for the initial admin account")
	fs.StringVar(&c.AdminPassword, "admin-password", os.Getenv("ADMIN_PASSWORD"), "password for the initial admin account")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	c.BindPort = bindPort
	c.MaxUploadMB = maxUpload
	if c.MaxUploadMB < 1 {
		c.MaxUploadMB = 1
	}
	if c.BindPort < 1 || c.BindPort > 65535 {
		c.BindPort = 8080
	}
	return c, nil
}
