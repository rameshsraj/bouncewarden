// Package model defines the snapshot schema shared by the collector,
// the console API and the embedded dashboard.
package model

import "time"

// SchemaVersion is bumped whenever the JSON event contract changes.
const SchemaVersion = 1

// Snapshot is one bounded collection across all configured instances.
type Snapshot struct {
	Schema    int         `json:"schema"`
	Seq       uint64      `json:"seq"`
	TakenAt   time.Time   `json:"taken_at"`
	Duration  int64       `json:"duration_ms"`
	Degraded  bool        `json:"degraded"`
	Instances []*Instance `json:"instances"`
	Findings  []*Finding  `json:"findings"`
}

// Instance is one observed PgBouncer process.
type Instance struct {
	Name        string    `json:"name"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	Enabled     bool      `json:"enabled"`
	Reachable   bool      `json:"reachable"`
	Error       string    `json:"error,omitempty"`
	Stale       bool      `json:"stale"`
	Version     string    `json:"version,omitempty"`
	CollectedAt time.Time `json:"collected_at"`

	Paused    bool `json:"paused"`
	Suspended bool `json:"suspended"`

	Caps map[string]bool `json:"caps,omitempty"`

	Pools     []*Pool           `json:"pools,omitempty"`
	Clients   []*Client         `json:"clients,omitempty"`
	Servers   []*Server         `json:"servers,omitempty"`
	Databases []*Database       `json:"databases,omitempty"`
	Stats     map[string]*Stats `json:"stats,omitempty"`
	Rates     map[string]*Rates `json:"rates,omitempty"`
	TotalRate *Rates            `json:"total_rate,omitempty"`
	Lists     map[string]int64  `json:"lists,omitempty"`
	Config    map[string]string `json:"config,omitempty"`
	Summary   *InstanceSummary  `json:"summary,omitempty"`
}

// InstanceSummary aggregates the counters most screens need.
type InstanceSummary struct {
	ClActive      int64   `json:"cl_active"`
	ClWaiting     int64   `json:"cl_waiting"`
	SvActive      int64   `json:"sv_active"`
	SvIdle        int64   `json:"sv_idle"`
	MaxWait       float64 `json:"max_wait"`
	ClientConns   int64   `json:"client_conns"`
	ServerConns   int64   `json:"server_conns"`
	MaxClientConn int64   `json:"max_client_conn"`
	FdUsed        int64   `json:"fd_used"`
	PoolCount     int     `json:"pool_count"`
	DatabaseCount int     `json:"database_count"`
	PoolUtil      float64 `json:"pool_util_pct"` // -1 unknown
	ClientUtil    float64 `json:"client_util_pct"`
}

// Pool mirrors SHOW POOLS for one (database,user) pair.
type Pool struct {
	Database           string  `json:"database"`
	User               string  `json:"user"`
	PoolMode           string  `json:"pool_mode,omitempty"`
	ClActive           int64   `json:"cl_active"`
	ClWaiting          int64   `json:"cl_waiting"`
	ClActiveCancelReq  int64   `json:"cl_active_cancel_req"`
	ClWaitingCancelReq int64   `json:"cl_waiting_cancel_req"`
	SvActive           int64   `json:"sv_active"`
	SvIdle             int64   `json:"sv_idle"`
	SvUsed             int64   `json:"sv_used"`
	SvTested           int64   `json:"sv_tested"`
	SvLogin            int64   `json:"sv_login"`
	MaxWait            float64 `json:"max_wait"` // seconds; -1 unknown
	LoadBalance        int64   `json:"load_balance"`
}

// Stats mirrors SHOW STATS counters (per database). On 1.21+ the totals
// come from SHOW STATS_TOTALS; ServerAssigns tracks the 1.23
// server_assignment_count counter.
type Stats struct {
	Database       string  `json:"database"`
	XactCount      int64   `json:"xact_count"`
	QueryCount     int64   `json:"query_count"`
	BytesReceived  int64   `json:"bytes_received"`
	BytesSent      int64   `json:"bytes_sent"`
	XactTimeUs     int64   `json:"xact_time_us"`
	QueryTimeUs    int64   `json:"query_time_us"`
	WaitTimeUs     int64   `json:"wait_time_us"`
	ServerAssigns  int64   `json:"server_assigns"`
	AvgXactCount   float64 `json:"avg_xact_count"` // server-provided interval average
	AvgQueryCount  float64 `json:"avg_query_count"`
	AvgRecv        float64 `json:"avg_recv"`
	AvgSent        float64 `json:"avg_sent"`
	AvgXactTimeUs  float64 `json:"avg_xact_time_us"`
	AvgQueryTimeUs float64 `json:"avg_query_time_us"`
	AvgWaitTimeUs  float64 `json:"avg_wait_time_us"` // -1 unknown
}

// Rates are derived from counter deltas by the collector.
// Window notes the measurement interval in seconds; values are -1 when
// the rate is unknown (counter reset, restart, first sample).
type Rates struct {
	XactPerSec     float64 `json:"xact_per_sec"`
	QueryPerSec    float64 `json:"query_per_sec"`
	BytesInPerSec  float64 `json:"bytes_in_per_sec"`
	BytesOutPerSec float64 `json:"bytes_out_per_sec"`
	Window         float64 `json:"window_s"`
}

// Client mirrors SHOW CLIENTS.
type Client struct {
	Type        string    `json:"type"`
	User        string    `json:"user"`
	Database    string    `json:"database"`
	State       string    `json:"state"`
	Addr        string    `json:"addr"`
	Port        int64     `json:"port"`
	LocalAddr   string    `json:"local_addr,omitempty"`
	LocalPort   int64     `json:"local_port,omitempty"`
	ConnectTime time.Time `json:"connect_time,omitempty"`
	RequestTime time.Time `json:"request_time,omitempty"`
	Wait        float64   `json:"wait"` // seconds; -1 unknown
	WaitUs      int64     `json:"wait_us"`
	CloseNeeded bool      `json:"close_needed"`
	Link        string    `json:"link,omitempty"`
	RemotePid   int64     `json:"remote_pid"`
	TLS         string    `json:"tls,omitempty"`
	AppName     string    `json:"app_name,omitempty"`
}

// Server mirrors SHOW SERVERS (one backend link).
type Server struct {
	Type        string    `json:"type"`
	User        string    `json:"user"`
	Database    string    `json:"database"`
	State       string    `json:"state"`
	Addr        string    `json:"addr"`
	Port        int64     `json:"port"`
	LocalAddr   string    `json:"local_addr,omitempty"`
	LocalPort   int64     `json:"local_port,omitempty"`
	ConnectTime time.Time `json:"connect_time,omitempty"`
	RequestTime time.Time `json:"request_time,omitempty"`
	Wait        float64   `json:"wait"`
	CloseNeeded bool      `json:"close_needed"`
	Link        string    `json:"link,omitempty"`
	RemotePid   int64     `json:"remote_pid"`
	TLS         string    `json:"tls,omitempty"`
}

// Database mirrors SHOW DATABASES.
type Database struct {
	Name                 string `json:"name"`
	Host                 string `json:"host,omitempty"`
	Port                 int64  `json:"port"`
	Database             string `json:"database"`
	ForceUser            string `json:"force_user,omitempty"`
	PoolSize             int64  `json:"pool_size"`
	MinPoolSize          int64  `json:"min_pool_size"`
	ReservePool          int64  `json:"reserve_pool"`
	PoolMode             string `json:"pool_mode,omitempty"`
	LoadBalance          int64  `json:"load_balance"`
	MaxConnections       int64  `json:"max_connections"`
	CurrentConnections   int64  `json:"current_connections"`
	MaxClientConnections int64  `json:"max_client_connections"`
	CurrentClientConns   int64  `json:"current_client_connections"`
	Paused               bool   `json:"paused"`
	Disabled             bool   `json:"disabled"`
}

// Finding is a diagnose rule hit with evidence.
type Finding struct {
	ID        string    `json:"id"`
	Severity  string    `json:"severity"` // info | warn | crit
	Instance  string    `json:"instance"`
	Pool      string    `json:"pool,omitempty"`
	Message   string    `json:"message"`
	Evidence  string    `json:"evidence,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}
