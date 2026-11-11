// Package config loads, validates and persists bouncewarden settings.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that marshals as "2s"-style strings in
// both YAML and JSON.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(v *yaml.Node) error {
	var s string
	if err := v.Decode(&s); err != nil {
		return err
	}
	if s == "" {
		return nil
	}
	if pd, err := time.ParseDuration(s); err == nil {
		*d = Duration(pd)
		return nil
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err == nil {
		*d = Duration(time.Duration(f * float64(time.Second)))
		return nil
	}
	return fmt.Errorf("invalid duration %q", s)
}

func (d Duration) MarshalYAML() (any, error) {
	return time.Duration(d).String(), nil
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		var n int64
		if err2 := json.Unmarshal(b, &n); err2 != nil {
			return err
		}
		*d = Duration(time.Duration(n))
		return nil
	}
	pd, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(pd)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Instance is one PgBouncer endpoint to monitor.
type Instance struct {
	Name     string `yaml:"name" json:"name"`
	Host     string `yaml:"host" json:"host"`
	Port     int    `yaml:"port" json:"port"`
	User     string `yaml:"user" json:"user"`
	Password string `yaml:"password,omitempty" json:"-"` // never marshalled to the browser
	DBName   string `yaml:"dbname" json:"dbname"`
	Enabled  bool   `yaml:"enabled" json:"enabled"`
	Admin    bool   `yaml:"admin" json:"admin"` // may run PAUSE/RELOAD/… verbs
	Env      string `yaml:"env,omitempty" json:"env,omitempty"`
}

// HasPassword reports whether a credential is stored (sent to the UI
// instead of the password itself).
func (i *Instance) HasPassword() bool { return i.Password != "" }

type Thresholds struct {
	WaitWarnMs         float64 `yaml:"wait_warn_ms" json:"wait_warn_ms"`
	WaitCritMs         float64 `yaml:"wait_crit_ms" json:"wait_crit_ms"`
	WaitingClientsWarn int64   `yaml:"waiting_clients_warn" json:"waiting_clients_warn"`
	FdWarnPct          float64 `yaml:"fd_warn_pct" json:"fd_warn_pct"`
	ClientUtilWarnPct  float64 `yaml:"client_util_warn_pct" json:"client_util_warn_pct"`
}

type Config struct {
	Listen         string      `yaml:"listen" json:"listen"`
	DataDir        string      `yaml:"data_dir" json:"data_dir"`
	PollInterval   Duration    `yaml:"poll_interval" json:"poll_interval"`
	ConnTimeout    Duration    `yaml:"conn_timeout" json:"conn_timeout"`
	HistorySamples int         `yaml:"history_samples" json:"history_samples"`
	AuthToken      string      `yaml:"auth_token,omitempty" json:"-"`
	Thresholds     Thresholds  `yaml:"thresholds" json:"thresholds"`
	Instances      []*Instance `yaml:"instances" json:"instances"`

	mu   sync.RWMutex `yaml:"-" json:"-"`
	path string       `yaml:"-" json:"-"`
}

func Defaults() *Config {
	return &Config{
		Listen:         ":8080",
		DataDir:        "/data",
		PollInterval:   Duration(2 * time.Second),
		ConnTimeout:    Duration(4 * time.Second),
		HistorySamples: 1800,
		Thresholds: Thresholds{
			WaitWarnMs:         50,
			WaitCritMs:         250,
			WaitingClientsWarn: 5,
			FdWarnPct:          80,
			ClientUtilWarnPct:  80,
		},
		Instances: []*Instance{},
	}
}

// Load reads path; a missing file returns defaults (first-run wizard mode).
func Load(path string) (*Config, error) {
	c := Defaults()
	c.path = path
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.PollInterval <= 0 {
		c.PollInterval = Duration(2 * time.Second)
	}
	if c.ConnTimeout <= 0 {
		c.ConnTimeout = Duration(4 * time.Second)
	}
	if c.HistorySamples <= 0 {
		c.HistorySamples = 1800
	}
	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.DataDir == "" {
		c.DataDir = filepath.Dir(path)
	}
	return c, nil
}

// Persist atomically writes the current config back to disk.
func (c *Config) Persist() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.persistLocked()
}

func (c *Config) persistLocked() error {
	if c.path == "" {
		return fmt.Errorf("no config path")
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// RLock/RLocker expose the read side for snapshot handlers.
func (c *Config) RLock()   { c.mu.RLock() }
func (c *Config) RUnlock() { c.mu.RUnlock() }

func (c *Config) Get(name string) *Instance {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, in := range c.Instances {
		if in.Name == name {
			return in
		}
	}
	return nil
}

// EnabledInstances returns a copy of enabled instance pointers.
func (c *Config) EnabledInstances() []*Instance {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Instance, 0, len(c.Instances))
	for _, in := range c.Instances {
		if in.Enabled {
			out = append(out, in)
		}
	}
	return out
}

// ListInstances returns instances sorted by name, passwords stripped.
func (c *Config) ListInstances() []*Instance {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*Instance, 0, len(c.Instances))
	for _, in := range c.Instances {
		cp := *in
		cp.Password = ""
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Upsert inserts or replaces an instance. If in.Password is empty and the
// instance already exists, the stored password is kept (write-only secrets).
func (c *Config) Upsert(in *Instance) error {
	if in.Name == "" || in.Host == "" {
		return fmt.Errorf("name and host are required")
	}
	if in.Port <= 0 || in.Port > 65535 {
		in.Port = 6432
	}
	if in.DBName == "" {
		in.DBName = "pgbouncer"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, ex := range c.Instances {
		if ex.Name == in.Name {
			if in.Password == "" {
				in.Password = ex.Password
			}
			c.Instances[i] = in
			return c.persistLocked()
		}
	}
	c.Instances = append(c.Instances, in)
	return c.persistLocked()
}

func (c *Config) Delete(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, ex := range c.Instances {
		if ex.Name == name {
			c.Instances = append(c.Instances[:i], c.Instances[i+1:]...)
			return c.persistLocked()
		}
	}
	return fmt.Errorf("instance %q not found", name)
}

func (c *Config) SetThresholds(t Thresholds) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Thresholds = t
	return c.persistLocked()
}
