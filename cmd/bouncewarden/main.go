// Command bouncewarden monitors PgBouncer. With `serve` it runs the web
// console (live dashboard, operations, configuration); the one-shot
// commands work without the console.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rameshsraj/bouncewarden/internal/collector"
	"github.com/rameshsraj/bouncewarden/internal/config"
	"github.com/rameshsraj/bouncewarden/internal/console"
	"github.com/rameshsraj/bouncewarden/internal/pgbadmin"
)

var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `bouncewarden %s — PgBouncer monitoring command centre

usage:
  bouncewarden serve   --config PATH [--listen ADDR]
  bouncewarden status  --config PATH
  bouncewarden monitor --config PATH
  bouncewarden probe   --host H --port P --user U [--password P] [--dbname D]
  bouncewarden version
`, version)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "status":
		cmdStatus(os.Args[2:])
	case "monitor":
		cmdMonitor(os.Args[2:])
	case "probe":
		cmdProbe(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("bouncewarden", version)
	default:
		usage()
	}
}

func loadConfig(path string) *config.Config {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	if v := os.Getenv("BOUNCEWARDEN_TOKEN"); v != "" {
		cfg.AuthToken = v
	}
	if v := os.Getenv("BOUNCEWARDEN_LISTEN"); v != "" {
		cfg.Listen = v
	}
	return cfg
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	confPath := fs.String("config", envOr("BOUNCEWARDEN_CONFIG", "/data/config.yaml"), "config path")
	listen := fs.String("listen", "", "override listen addr")
	fs.Parse(args)

	cfg := loadConfig(*confPath)
	if *listen != "" {
		cfg.Listen = *listen
	}

	col := collector.New(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go col.Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           console.New(cfg, col).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	fmt.Printf("bouncewarden %s console on http://%s (config %s)\n", version, cfg.Listen, *confPath)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	confPath := fs.String("config", envOr("BOUNCEWARDEN_CONFIG", "config.yaml"), "config path")
	fs.Parse(args)
	cfg := loadConfig(*confPath)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ConnTimeout)*2)
	defer cancel()
	for _, in := range cfg.EnabledInstances() {
		p := pgbadmin.Probe(ctx, pgbadmin.Conn{
			Host: in.Host, Port: in.Port, User: in.User,
			Password: in.Password, DBName: in.DBName, Timeout: time.Duration(cfg.ConnTimeout),
		})
		if !p.OK {
			fmt.Printf("%-12s %-22s UNREACHABLE  %s\n", in.Name, in.Host, p.Error)
			continue
		}
		fmt.Printf("%-12s %-22s OK  %s\n", in.Name, in.Host, p.Version)
	}
}

func cmdMonitor(args []string) {
	fs := flag.NewFlagSet("monitor", flag.ExitOnError)
	confPath := fs.String("config", envOr("BOUNCEWARDEN_CONFIG", "config.yaml"), "config path")
	fs.Parse(args)
	cfg := loadConfig(*confPath)

	col := collector.New(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ch, unsub := col.Subscribe()
	defer unsub()
	go col.Run(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-ch:
			var ev collector.Event
			if json.Unmarshal(b, &ev) == nil && ev.Kind == "snapshot" {
				fmt.Println(string(b))
			}
		}
	}
}

func cmdProbe(args []string) {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	host := fs.String("host", "127.0.0.1", "host")
	port := fs.Int("port", 6432, "port")
	user := fs.String("user", "", "admin console user")
	pw := fs.String("password", "", "admin console password")
	db := fs.String("dbname", "pgbouncer", "admin database")
	fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	res := pgbadmin.Probe(ctx, pgbadmin.Conn{
		Host: *host, Port: *port, User: *user, Password: *pw,
		DBName: *db, Timeout: 5 * time.Second,
	})
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
	if !res.OK {
		os.Exit(10)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
