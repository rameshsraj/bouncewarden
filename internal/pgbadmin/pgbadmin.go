// Package pgbadmin talks to the PgBouncer admin console over the psql
// wire protocol. The admin console only supports the simple query
// protocol, so every query runs in simple mode.
package pgbadmin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Conn describes one admin-console endpoint.
type Conn struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	Timeout  time.Duration
}

// OpVerbs are the admin verbs the Operations console may run.
var OpVerbs = map[string]bool{
	"PAUSE": true, "RESUME": true, "SUSPEND": true, "RELOAD": true,
	"SHUTDOWN": true, "DISABLE": true, "ENABLE": true, "RECONNECT": true,
	"KILL_DB": true, "WAIT_CLOSE": true,
}

// VerbsTakingDatabase may be scoped to one database.
var VerbsTakingDatabase = map[string]bool{
	"PAUSE": true, "RESUME": true, "DISABLE": true, "ENABLE": true,
	"RECONNECT": true, "KILL_DB": true, "WAIT_CLOSE": true,
}

func connect(ctx context.Context, c Conn) (*pgx.Conn, error) {
	db := c.DBName
	if db == "" {
		db = "pgbouncer"
	}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable&connect_timeout=%d",
		urlQueryEscape(c.User), urlQueryEscape(c.Password), c.Host, c.Port, db,
		int(c.Timeout.Seconds()))
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	// PgBouncer's admin console rejects the extended query protocol.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	cfg.ConnectTimeout = c.Timeout
	return pgx.ConnectConfig(ctx, cfg)
}

func urlQueryEscape(s string) string {
	r := strings.NewReplacer(
		"%", "%25", " ", "%20", "#", "%23", "$", "%24", "&", "%26",
		"+", "%2B", "/", "%2F", ":", "%3A", "?", "%3F", "@", "%40",
		"[", "%5B", "]", "%5D", "=", "%3D",
	)
	return r.Replace(s)
}

// Row is one result row keyed by column name.
type Row map[string]any

// Query runs a simple-protocol query and returns generic rows.
// A fresh connection is used per call so a pooler restart never wedges
// a long-lived session; failures stay isolated to this call.
func Query(ctx context.Context, c Conn, sql string) ([]Row, error) {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout+time.Second)
	defer cancel()

	conn, err := connect(ctx, c)
	if err != nil {
		return nil, err
	}
	defer conn.Close(context.Background())

	rows, err := conn.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fds := rows.FieldDescriptions()
	out := []Row{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		r := make(Row, len(fds))
		for i, fd := range fds {
			r[string(fd.Name)] = normalize(vals[i])
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Exec runs an admin verb (PAUSE, RESUME, …) and returns the server's
// reply text, e.g. "PAUSE".
func Exec(ctx context.Context, c Conn, verb, database string) (string, error) {
	verb = strings.ToUpper(strings.TrimSpace(verb))
	if !OpVerbs[verb] {
		return "", fmt.Errorf("verb %q not allowed", verb)
	}
	sql := verb
	if database != "" {
		if !VerbsTakingDatabase[verb] {
			return "", fmt.Errorf("%s does not take a database argument", verb)
		}
		// Database names come from SHOW DATABASES; still, refuse quotes.
		if strings.ContainsAny(database, `";' `+"\t\n") {
			return "", fmt.Errorf("invalid database name %q", database)
		}
		sql = verb + " " + database
	}
	rows, err := Query(ctx, c, sql)
	if err != nil {
		return "", err
	}
	if len(rows) > 0 {
		for _, v := range rows[0] {
			return fmt.Sprint(v), nil
		}
	}
	return sql, nil
}

// Probe connects, reads SHOW VERSION and reports which SHOW commands the
// deployed version supports. Used by onboarding and source health.
type ProbeResult struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error,omitempty"`
	Version string          `json:"version,omitempty"`
	Caps    map[string]bool `json:"caps"`
}

func Probe(ctx context.Context, c Conn) *ProbeResult {
	p := &ProbeResult{Caps: map[string]bool{}}
	rows, err := Query(ctx, c, "SHOW VERSION")
	if err != nil {
		p.Error = err.Error()
		return p
	}
	p.OK = true
	for _, r := range rows {
		for _, v := range r {
			p.Version = fmt.Sprint(v)
		}
	}
	p.Caps = Capabilities(p.Version)
	return p
}

// Capabilities maps a "PgBouncer 1.x.y" version string to the SHOW
// commands and fields that version provides.
func Capabilities(version string) map[string]bool {
	minor := parseMinor(version)
	return map[string]bool{
		"stats_totals":   minor >= 21,
		"stats_averages": minor >= 21,
		"avg_wait_time":  minor >= 21,
		"sockets":        minor >= 18,
		"mempool":        minor >= 18,
		"dns_hosts":      minor >= 19,
		"dns_zones":      minor >= 19,
		"fds":            minor >= 21,
		"maxwait_us":     minor >= 21,
	}
}

func parseMinor(version string) int {
	// "PgBouncer 1.24.1" → 24 ; "PgBouncer 2.0" → 99
	var maj, min int
	fields := strings.Fields(version)
	for _, f := range fields {
		if n, err := fmt.Sscanf(f, "%d.%d", &maj, &min); err == nil && n == 2 {
			if maj > 1 {
				return 99
			}
			return min
		}
	}
	return 0
}

func normalize(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(t)
	case int16:
		return int64(t)
	case int32:
		return int64(t)
	case float32:
		return float64(t)
	default:
		return v
	}
}

// Helpers to coerce generic Row values.
func Str(r Row, k string) string {
	if v, ok := r[k]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

func Int(r Row, k string) int64 {
	v, ok := r[k]
	if !ok || v == nil {
		return 0
	}
	switch t := v.(type) {
	case int64:
		return t
	case float64:
		return int64(t)
	case pgtype.Numeric:
		// PgBouncer reports SHOW counters as numeric (oid 1700).
		if iv, err := t.Int64Value(); err == nil && iv.Valid {
			return iv.Int64
		}
	case string:
		var n int64
		fmt.Sscanf(t, "%d", &n)
		return n
	}
	return 0
}

func Float(r Row, k string) float64 {
	v, ok := r[k]
	if !ok || v == nil {
		return -1
	}
	switch t := v.(type) {
	case float64:
		return t
	case int64:
		return float64(t)
	case pgtype.Numeric:
		if fv, err := t.Float64Value(); err == nil && fv.Valid {
			return fv.Float64
		}
	case string:
		var f float64
		if _, err := fmt.Sscanf(t, "%g", &f); err != nil {
			return -1
		}
		return f
	}
	return -1
}

func Has(r Row, k string) bool {
	_, ok := r[k]
	return ok
}
