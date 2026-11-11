// Package collector polls every enabled PgBouncer instance on one shared
// cadence, derives rates from counter deltas, evaluates findings and
// fans snapshots out to SSE subscribers.
package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rameshsraj/bouncewarden/internal/config"
	"github.com/rameshsraj/bouncewarden/internal/model"
	"github.com/rameshsraj/bouncewarden/internal/pgbadmin"
)

// HistoryPoint is the compact per-tick record kept for charts.
type HistoryPoint struct {
	TakenAt   time.Time                 `json:"taken_at"`
	Instances map[string]*InstancePoint `json:"instances"`
}

type InstancePoint struct {
	Reachable   bool    `json:"reachable"`
	ClActive    int64   `json:"cl_active"`
	ClWaiting   int64   `json:"cl_waiting"`
	SvActive    int64   `json:"sv_active"`
	SvIdle      int64   `json:"sv_idle"`
	MaxWait     float64 `json:"max_wait"`
	XactPerSec  float64 `json:"xact_per_sec"`
	QueryPerSec float64 `json:"query_per_sec"`
	BytesIn     float64 `json:"bytes_in_per_sec"`
	BytesOut    float64 `json:"bytes_out_per_sec"`
}

// AuditEntry is one journaled operations-console action.
type AuditEntry struct {
	At       time.Time `json:"at"`
	Actor    string    `json:"actor"`
	Instance string    `json:"instance"`
	Verb     string    `json:"verb"`
	Database string    `json:"database,omitempty"`
	OK       bool      `json:"ok"`
	Result   string    `json:"result,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// Event is the SSE payload envelope.
type Event struct {
	Kind string `json:"kind"` // snapshot | audit | hello
	Data any    `json:"data"`
}

type subscriber chan []byte

// Collector owns the shared monitor loop.
type Collector struct {
	cfg *config.Config

	mu        sync.RWMutex
	latest    *model.Snapshot
	seq       uint64
	hist      []HistoryPoint
	prev      map[string]*prevStats
	findings  map[string]*model.Finding
	audit     []AuditEntry
	subs      map[subscriber]struct{}
	collectCh chan struct{}
	auditPath string
}

type prevStats struct {
	at  time.Time
	dbs map[string]*model.Stats
}

func New(cfg *config.Config) *Collector {
	return &Collector{
		cfg:       cfg,
		prev:      map[string]*prevStats{},
		findings:  map[string]*model.Finding{},
		subs:      map[subscriber]struct{}{},
		collectCh: make(chan struct{}, 1),
		auditPath: filepath.Join(cfg.DataDir, "audit.jsonl"),
	}
}

// Latest returns the newest snapshot, or nil before the first cycle.
func (c *Collector) Latest() *model.Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latest
}

// History returns up to n most recent points, oldest first.
func (c *Collector) History(n int) []HistoryPoint {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if n <= 0 || n > len(c.hist) {
		n = len(c.hist)
	}
	out := make([]HistoryPoint, n)
	copy(out, c.hist[len(c.hist)-n:])
	return out
}

func (c *Collector) Audit() []AuditEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]AuditEntry, len(c.audit))
	copy(out, c.audit)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Subscribe registers an SSE channel. The latest snapshot is replayed.
func (c *Collector) Subscribe() (subscriber, func()) {
	ch := make(subscriber, 8)
	c.mu.Lock()
	c.subs[ch] = struct{}{}
	latest := c.latest
	c.mu.Unlock()
	if latest != nil {
		if b, err := json.Marshal(Event{Kind: "snapshot", Data: latest}); err == nil {
			ch <- b
		}
	}
	return ch, func() {
		c.mu.Lock()
		delete(c.subs, ch)
		c.mu.Unlock()
	}
}

func (c *Collector) publish(kind string, data any) {
	b, err := json.Marshal(Event{Kind: kind, Data: data})
	if err != nil {
		return
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for ch := range c.subs {
		select {
		case ch <- b:
		default: // slow client must never stall collection
		}
	}
}

// Run starts the shared monitor loop until ctx ends.
func (c *Collector) Run(ctx context.Context) {
	c.collectOnce(ctx)
	t := time.NewTicker(time.Duration(c.cfg.PollInterval))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.collectOnce(ctx)
		case <-c.collectCh:
			c.collectOnce(ctx)
		}
	}
}

// Kick requests an out-of-cycle collection (after an op).
func (c *Collector) Kick() {
	select {
	case c.collectCh <- struct{}{}:
	default:
	}
}

func (c *Collector) collectOnce(ctx context.Context) {
	start := time.Now()
	c.seq++
	snap := &model.Snapshot{
		Schema:  model.SchemaVersion,
		Seq:     c.seq,
		TakenAt: start.UTC(),
	}
	for _, in := range c.cfg.EnabledInstances() {
		snap.Instances = append(snap.Instances, c.collectInstance(ctx, in))
	}
	snap.Duration = time.Since(start).Milliseconds()

	c.evaluate(snap)

	point := HistoryPoint{TakenAt: snap.TakenAt, Instances: map[string]*InstancePoint{}}
	for _, in := range snap.Instances {
		ip := &InstancePoint{Reachable: in.Reachable}
		if in.Summary != nil {
			ip.ClActive = in.Summary.ClActive
			ip.ClWaiting = in.Summary.ClWaiting
			ip.SvActive = in.Summary.SvActive
			ip.SvIdle = in.Summary.SvIdle
			ip.MaxWait = in.Summary.MaxWait
		}
		if in.TotalRate != nil {
			ip.XactPerSec = in.TotalRate.XactPerSec
			ip.QueryPerSec = in.TotalRate.QueryPerSec
			ip.BytesIn = in.TotalRate.BytesInPerSec
			ip.BytesOut = in.TotalRate.BytesOutPerSec
		}
		point.Instances[in.Name] = ip
	}

	c.mu.Lock()
	for _, in := range snap.Instances {
		if in.Reachable {
			continue
		}
		// keep prior last-known data marked stale
		if c.latest != nil {
			for _, old := range c.latest.Instances {
				if old.Name == in.Name && old.Reachable {
					old.Stale = true
					*in = *old
					in.Reachable = false
					in.Stale = true
					if in.Error == "" {
						in.Error = "unreachable; showing last known values"
					}
				}
			}
		}
	}
	c.latest = snap
	c.hist = append(c.hist, point)
	if n := c.cfg.HistorySamples; len(c.hist) > n {
		c.hist = c.hist[len(c.hist)-n:]
	}
	c.mu.Unlock()

	c.publish("snapshot", snap)
}

func (c *Collector) collectInstance(ctx context.Context, in *config.Instance) *model.Instance {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.cfg.ConnTimeout)*2)
	defer cancel()

	conn := pgbadmin.Conn{
		Host: in.Host, Port: in.Port, User: in.User,
		Password: in.Password, DBName: in.DBName, Timeout: time.Duration(c.cfg.ConnTimeout),
	}
	out := &model.Instance{
		Name: in.Name, Host: in.Host, Port: in.Port, Enabled: in.Enabled,
		CollectedAt: time.Now().UTC(),
	}

	ver, err := pgbadmin.Query(ctx, conn, "SHOW VERSION")
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Reachable = true
	if len(ver) > 0 {
		for _, v := range ver[0] {
			out.Version = fmt.Sprint(v)
		}
	}

	caps := pgbadmin.Capabilities(out.Version)
	out.Caps = caps

	if rows, err := pgbadmin.Query(ctx, conn, "SHOW POOLS"); err == nil {
		for _, r := range rows {
			p := &model.Pool{
				Database:           pgbadmin.Str(r, "database"),
				User:               pgbadmin.Str(r, "user"),
				PoolMode:           pgbadmin.Str(r, "pool_mode"),
				ClActive:           pgbadmin.Int(r, "cl_active"),
				ClWaiting:          pgbadmin.Int(r, "cl_waiting"),
				ClActiveCancelReq:  pgbadmin.Int(r, "cl_active_cancel_req"),
				ClWaitingCancelReq: pgbadmin.Int(r, "cl_waiting_cancel_req"),
				SvActive:           pgbadmin.Int(r, "sv_active"),
				SvIdle:             pgbadmin.Int(r, "sv_idle"),
				SvUsed:             pgbadmin.Int(r, "sv_used"),
				SvTested:           pgbadmin.Int(r, "sv_tested"),
				SvLogin:            pgbadmin.Int(r, "sv_login"),
				MaxWait:            pgbadmin.Float(r, "maxwait"),
				LoadBalance:        pgbadmin.Int(r, "load_balance"),
			}
			if pgbadmin.Has(r, "maxwait_us") {
				if us := pgbadmin.Int(r, "maxwait_us"); us > 0 {
					p.MaxWait = float64(pgbadmin.Int(r, "maxwait")) + float64(us)/1e6
				}
			}
			out.Pools = append(out.Pools, p)
		}
	}

	// SHOW STATS carries total_* + avg_* on every version; intOr also
	// accepts the unprefixed names used by STATS_TOTALS (1.21+).
	if rows, err := pgbadmin.Query(ctx, conn, "SHOW STATS"); err == nil {
		out.Stats = map[string]*model.Stats{}
		for _, r := range rows {
			s := &model.Stats{
				Database:       pgbadmin.Str(r, "database"),
				XactCount:      intOr(r, "total_xact_count", "xact_count"),
				QueryCount:     intOr(r, "total_query_count", "query_count"),
				BytesReceived:  intOr(r, "total_received", "bytes_received"),
				BytesSent:      intOr(r, "total_sent", "bytes_sent"),
				XactTimeUs:     intOr(r, "total_xact_time", "xact_time"),
				QueryTimeUs:    intOr(r, "total_query_time", "query_time"),
				WaitTimeUs:     intOr(r, "total_wait_time", "wait_time"),
				ServerAssigns:  intOr(r, "total_server_assignment_count", "server_assignment_count"),
				AvgXactCount:   pgbadmin.Float(r, "avg_xact_count"),
				AvgQueryCount:  pgbadmin.Float(r, "avg_query_count"),
				AvgRecv:        pgbadmin.Float(r, "avg_recv"),
				AvgSent:        pgbadmin.Float(r, "avg_sent"),
				AvgXactTimeUs:  pgbadmin.Float(r, "avg_xact_time"),
				AvgQueryTimeUs: pgbadmin.Float(r, "avg_query_time"),
				AvgWaitTimeUs:  pgbadmin.Float(r, "avg_wait_time"),
			}
			out.Stats[s.Database] = s
		}
	}
	out.Rates, out.TotalRate = c.deriveRates(in.Name, out.Stats, out.CollectedAt)

	if rows, err := pgbadmin.Query(ctx, conn, "SHOW CLIENTS"); err == nil {
		for _, r := range rows {
			out.Clients = append(out.Clients, &model.Client{
				Type:        pgbadmin.Str(r, "type"),
				User:        pgbadmin.Str(r, "user"),
				Database:    pgbadmin.Str(r, "database"),
				State:       pgbadmin.Str(r, "state"),
				Addr:        pgbadmin.Str(r, "addr"),
				Port:        pgbadmin.Int(r, "port"),
				LocalAddr:   pgbadmin.Str(r, "local_addr"),
				LocalPort:   pgbadmin.Int(r, "local_port"),
				ConnectTime: parseTime(pgbadmin.Str(r, "connect_time")),
				RequestTime: parseTime(pgbadmin.Str(r, "request_time")),
				Wait:        pgbadmin.Float(r, "wait"),
				WaitUs:      pgbadmin.Int(r, "wait_us"),
				Link:        pgbadmin.Str(r, "link"),
				RemotePid:   pgbadmin.Int(r, "remote_pid"),
				TLS:         pgbadmin.Str(r, "tls"),
				AppName:     pgbadmin.Str(r, "application_name"),
			})
		}
	}

	if rows, err := pgbadmin.Query(ctx, conn, "SHOW SERVERS"); err == nil {
		for _, r := range rows {
			out.Servers = append(out.Servers, &model.Server{
				Type:        pgbadmin.Str(r, "type"),
				User:        pgbadmin.Str(r, "user"),
				Database:    pgbadmin.Str(r, "database"),
				State:       pgbadmin.Str(r, "state"),
				Addr:        pgbadmin.Str(r, "addr"),
				Port:        pgbadmin.Int(r, "port"),
				LocalAddr:   pgbadmin.Str(r, "local_addr"),
				LocalPort:   pgbadmin.Int(r, "local_port"),
				ConnectTime: parseTime(pgbadmin.Str(r, "connect_time")),
				RequestTime: parseTime(pgbadmin.Str(r, "request_time")),
				CloseNeeded: pgbadmin.Int(r, "close_needed") != 0,
				Link:        pgbadmin.Str(r, "link"),
				RemotePid:   pgbadmin.Int(r, "remote_pid"),
				TLS:         pgbadmin.Str(r, "tls"),
			})
		}
	}

	if rows, err := pgbadmin.Query(ctx, conn, "SHOW DATABASES"); err == nil {
		for _, r := range rows {
			d := &model.Database{
				Name:                 pgbadmin.Str(r, "name"),
				Host:                 pgbadmin.Str(r, "host"),
				Port:                 pgbadmin.Int(r, "port"),
				Database:             pgbadmin.Str(r, "database"),
				ForceUser:            pgbadmin.Str(r, "force_user"),
				PoolSize:             pgbadmin.Int(r, "pool_size"),
				MinPoolSize:          pgbadmin.Int(r, "min_pool_size"),
				ReservePool:          pgbadmin.Int(r, "reserve_pool"),
				PoolMode:             pgbadmin.Str(r, "pool_mode"),
				LoadBalance:          pgbadmin.Int(r, "load_balance"),
				MaxConnections:       pgbadmin.Int(r, "max_connections"),
				CurrentConnections:   pgbadmin.Int(r, "current_connections"),
				MaxClientConnections: pgbadmin.Int(r, "max_client_connections"),
				CurrentClientConns:   pgbadmin.Int(r, "current_client_connections"),
				Paused:               pgbadmin.Int(r, "paused") != 0,
				Disabled:             pgbadmin.Int(r, "disabled") != 0,
			}
			out.Databases = append(out.Databases, d)
			if d.Paused {
				out.Paused = true
			}
			if d.Disabled {
				// recorded per-database; instance flag stays false
			}
		}
	}

	if rows, err := pgbadmin.Query(ctx, conn, "SHOW LISTS"); err == nil {
		out.Lists = map[string]int64{}
		for _, r := range rows {
			out.Lists[pgbadmin.Str(r, "list")] = pgbadmin.Int(r, "items")
		}
	}

	// SHOW STATE reports global active/paused/suspended; the per-database
	// paused flag in SHOW DATABASES only reflects PAUSE <db>.
	if rows, err := pgbadmin.Query(ctx, conn, "SHOW STATE"); err == nil {
		for _, r := range rows {
			switch pgbadmin.Str(r, "key") {
			case "paused":
				out.Paused = out.Paused || pgbadmin.Str(r, "value") == "yes"
			case "suspended":
				out.Suspended = pgbadmin.Str(r, "value") == "yes"
			}
		}
	}

	if rows, err := pgbadmin.Query(ctx, conn, "SHOW CONFIG"); err == nil {
		out.Config = map[string]string{}
		for _, r := range rows {
			k := pgbadmin.Str(r, "key")
			v := pgbadmin.Str(r, "value")
			lk := strings.ToLower(k)
			if strings.Contains(lk, "pass") || strings.Contains(lk, "secret") ||
				lk == "auth_query" {
				v = "[redacted]"
			}
			out.Config[k] = v
		}
	}

	out.Summary = summarize(out)
	return out
}

func summarize(in *model.Instance) *model.InstanceSummary {
	s := &model.InstanceSummary{PoolUtil: -1, MaxWait: -1}
	s.PoolCount = len(in.Pools)
	s.DatabaseCount = len(in.Databases)
	var poolCap int64
	for _, p := range in.Pools {
		s.ClActive += p.ClActive
		s.ClWaiting += p.ClWaiting
		s.SvActive += p.SvActive
		s.SvIdle += p.SvIdle
		if p.MaxWait > s.MaxWait {
			s.MaxWait = p.MaxWait
		}
	}
	for _, d := range in.Databases {
		poolCap += d.PoolSize
	}
	s.ClientConns = int64(len(in.Clients))
	s.ServerConns = int64(len(in.Servers))
	if v, ok := in.Config["max_client_conn"]; ok {
		fmt.Sscanf(v, "%d", &s.MaxClientConn)
	}
	if s.MaxClientConn > 0 {
		s.ClientUtil = float64(s.ClientConns) / float64(s.MaxClientConn) * 100
	}
	if poolCap > 0 && s.SvActive+s.SvIdle > 0 {
		s.PoolUtil = float64(s.SvActive+s.SvIdle) / float64(poolCap) * 100
	}
	return s
}

// deriveRates computes per-second rates from counter deltas. Rates are
// -1 when unknown: first sample, counter reset (restart), or new pool.
func (c *Collector) deriveRates(name string, stats map[string]*model.Stats, now time.Time) (map[string]*model.Rates, *model.Rates) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, ok := c.prev[name]
	cur := &prevStats{at: now, dbs: map[string]*model.Stats{}}
	for db, s := range stats {
		cp := *s
		cur.dbs[db] = &cp
	}
	c.prev[name] = cur

	rates := map[string]*model.Rates{}
	total := &model.Rates{XactPerSec: -1, QueryPerSec: -1, BytesInPerSec: -1, BytesOutPerSec: -1}
	if !ok || prev == nil {
		return rates, total
	}
	dt := now.Sub(prev.at).Seconds()
	if dt <= 0 {
		return rates, total
	}
	var tx, qx, bi, bo float64
	valid := false
	for db, s := range stats {
		p, had := prev.dbs[db]
		r := &model.Rates{XactPerSec: -1, QueryPerSec: -1, BytesInPerSec: -1, BytesOutPerSec: -1, Window: dt}
		if had && s.XactCount >= p.XactCount && s.QueryCount >= p.QueryCount &&
			s.BytesReceived >= p.BytesReceived && s.BytesSent >= p.BytesSent {
			r.XactPerSec = float64(s.XactCount-p.XactCount) / dt
			r.QueryPerSec = float64(s.QueryCount-p.QueryCount) / dt
			r.BytesInPerSec = float64(s.BytesReceived-p.BytesReceived) / dt
			r.BytesOutPerSec = float64(s.BytesSent-p.BytesSent) / dt
			tx += r.XactPerSec
			qx += r.QueryPerSec
			bi += r.BytesInPerSec
			bo += r.BytesOutPerSec
			valid = true
		}
		rates[db] = r
	}
	if valid {
		total.XactPerSec, total.QueryPerSec = tx, qx
		total.BytesInPerSec, total.BytesOutPerSec = bi, bo
		total.Window = dt
	}
	return rates, total
}

// evaluate runs the findings rule set over one snapshot.
func (c *Collector) evaluate(snap *model.Snapshot) {
	th := c.cfg.Thresholds
	active := map[string]bool{}

	fire := func(id, sev, inst, pool, msg, ev string) {
		key := inst + "|" + id + "|" + pool
		active[key] = true
		if f, ok := c.findings[key]; ok {
			f.LastSeen = snap.TakenAt
			return
		}
		c.findings[key] = &model.Finding{
			ID: id, Severity: sev, Instance: inst, Pool: pool,
			Message: msg, Evidence: ev,
			FirstSeen: snap.TakenAt, LastSeen: snap.TakenAt,
		}
	}

	for _, in := range snap.Instances {
		if !in.Reachable && !in.Stale {
			fire("UNREACHABLE", "crit", in.Name, "", "instance unreachable", in.Error)
			continue
		}
		if in.Paused {
			fire("PAUSED", "warn", in.Name, "", "pooler is paused", "SHOW DATABASES paused=1")
		}
		if in.Suspended {
			fire("SUSPENDED", "warn", in.Name, "", "pooler is suspended", "SHOW LISTS suspended_clients>0")
		}
		if in.Summary != nil {
			if th.ClientUtilWarnPct > 0 && in.Summary.ClientUtil >= th.ClientUtilWarnPct {
				fire("MAX_CLIENT_CONN_SATURATION", "warn", in.Name, "",
					fmt.Sprintf("client connections at %.0f%% of max_client_conn", in.Summary.ClientUtil),
					fmt.Sprintf("%d/%d", in.Summary.ClientConns, in.Summary.MaxClientConn))
			}
			if th.WaitCritMs > 0 && in.Summary.MaxWait*1000 >= th.WaitCritMs {
				fire("MAXWAIT_HIGH", "crit", in.Name, "",
					fmt.Sprintf("client wait %.0fms exceeds crit threshold", in.Summary.MaxWait*1000),
					fmt.Sprintf("crit>=%.0fms", th.WaitCritMs))
			} else if th.WaitWarnMs > 0 && in.Summary.MaxWait*1000 >= th.WaitWarnMs {
				fire("MAXWAIT_HIGH", "warn", in.Name, "",
					fmt.Sprintf("client wait %.0fms exceeds warn threshold", in.Summary.MaxWait*1000),
					fmt.Sprintf("warn>=%.0fms", th.WaitWarnMs))
			}
		}
		for _, p := range in.Pools {
			poolKey := p.Database + "/" + p.User
			if th.WaitingClientsWarn > 0 && p.ClWaiting >= th.WaitingClientsWarn {
				fire("CLIENTS_WAITING", "warn", in.Name, poolKey,
					fmt.Sprintf("%d clients waiting in %s", p.ClWaiting, poolKey),
					"SHOW POOLS cl_waiting")
			}
			for _, d := range in.Databases {
				if d.Name != p.Database {
					continue
				}
				if d.PoolSize > 0 && p.SvActive+p.SvIdle >= d.PoolSize && p.ClWaiting > 0 {
					fire("POOL_SATURATED", "crit", in.Name, poolKey,
						fmt.Sprintf("pool %s saturated (%d/%d servers) while %d clients wait",
							poolKey, p.SvActive+p.SvIdle, d.PoolSize, p.ClWaiting),
						"SHOW POOLS vs SHOW DATABASES pool_size")
				}
				if d.Paused {
					fire("PAUSED_DB", "warn", in.Name, poolKey,
						fmt.Sprintf("database %s is paused", d.Name), "SHOW DATABASES paused=1")
				}
				if d.Disabled {
					fire("DISABLED_DB", "crit", in.Name, poolKey,
						fmt.Sprintf("database %s is disabled", d.Name), "SHOW DATABASES disabled=1")
				}
			}
			if p.SvLogin > 0 {
				fire("SERVER_LOGIN_STUCK", "warn", in.Name, poolKey,
					fmt.Sprintf("%d server logins in progress in %s", p.SvLogin, poolKey),
					"SHOW POOLS sv_login>0")
			}
		}
	}

	snap.Findings = snap.Findings[:0]
	for k, f := range c.findings {
		if active[k] {
			snap.Findings = append(snap.Findings, f)
		} else {
			delete(c.findings, k)
		}
	}
	for _, in := range snap.Instances {
		if in.Error != "" {
			snap.Degraded = true
		}
	}
}

// intOr returns the first non-missing integer column value.
func intOr(r pgbadmin.Row, keys ...string) int64 {
	for _, k := range keys {
		if pgbadmin.Has(r, k) {
			return pgbadmin.Int(r, k)
		}
	}
	return 0
}

func parseTime(s string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999 MST",
		"2006-01-02 15:04:05 MST",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// ExecOp runs an admin verb against one instance, audits the result and
// kicks an immediate re-collection.
func (c *Collector) ExecOp(ctx context.Context, actor, instName, verb, database string) error {
	in := c.cfg.Get(instName)
	if in == nil {
		return fmt.Errorf("instance %q not found", instName)
	}
	if !in.Admin {
		return fmt.Errorf("instance %q has no admin credential configured", instName)
	}
	entry := AuditEntry{
		At: time.Now().UTC(), Actor: actor, Instance: instName,
		Verb: strings.ToUpper(verb), Database: database,
	}
	conn := pgbadmin.Conn{
		Host: in.Host, Port: in.Port, User: in.User,
		Password: in.Password, DBName: in.DBName, Timeout: time.Duration(c.cfg.ConnTimeout),
	}
	res, err := pgbadmin.Exec(ctx, conn, verb, database)
	if err != nil {
		entry.Error = err.Error()
	} else {
		entry.OK = true
		entry.Result = res
	}
	c.logAudit(entry)
	c.Kick()
	return err
}

func (c *Collector) logAudit(e AuditEntry) {
	c.mu.Lock()
	c.audit = append(c.audit, e)
	if len(c.audit) > 500 {
		c.audit = c.audit[len(c.audit)-500:]
	}
	c.mu.Unlock()

	if b, err := json.Marshal(e); err == nil {
		if f, err := os.OpenFile(c.auditPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
	c.publish("audit", e)
}

// LogEvent records a non-op audit entry (config changes etc.).
func (c *Collector) LogEvent(actor, instance, verb, result string) {
	c.logAudit(AuditEntry{
		At: time.Now().UTC(), Actor: actor, Instance: instance,
		Verb: verb, OK: true, Result: result,
	})
}
