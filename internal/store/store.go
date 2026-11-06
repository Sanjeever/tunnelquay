package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Sanjeever/tunnelquay/internal/protocol"
	_ "modernc.org/sqlite"
)

type Store struct{ DB *sql.DB }
type Device struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Hostname     string    `json:"hostname"`
	OS           string    `json:"os"`
	Arch         string    `json:"arch"`
	Version      string    `json:"version"`
	LastSeen     time.Time `json:"last_seen"`
	Capabilities []string  `json:"capabilities"`
	Online       bool      `json:"online"`
	Transport    string    `json:"transport"`
	RTT          *float64  `json:"rtt"`
}
type Tunnel struct {
	Desired      protocol.Config  `json:"desired"`
	Applied      *protocol.Config `json:"applied"`
	Error        string           `json:"error"`
	Status       string           `json:"status"`
	TargetStatus string           `json:"target_status"`
	Connections  int              `json:"connections"`
	Up           int64            `json:"up"`
	Down         int64            `json:"down"`
	UpRate       float64          `json:"up_rate"`
	DownRate     float64          `json:"down_rate"`
}
type Connection struct {
	ID         string     `json:"id"`
	DeviceID   string     `json:"device_id"`
	DeviceName string     `json:"device_name"`
	TunnelID   string     `json:"tunnel_id"`
	TunnelName string     `json:"tunnel_name"`
	Revision   int64      `json:"revision"`
	Protocol   string     `json:"protocol"`
	Source     string     `json:"source"`
	Target     string     `json:"target"`
	Started    time.Time  `json:"started"`
	Ended      *time.Time `json:"ended"`
	Up         int64      `json:"up"`
	Down       int64      `json:"down"`
	Result     string     `json:"result"`
	Reason     string     `json:"reason"`
}
type Filter struct {
	DeviceID, TunnelID, Protocol, Source, From, To, State string
	Page, Limit                                           int
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "tunnelquay.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db}
	fail := func(e error) (*Store, error) { db.Close(); return nil, e }
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON;`); err != nil {
		return fail(err)
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version > 1 {
		return fail(errors.New("数据库版本高于当前程序支持范围"))
	}
	if version == 0 {
		tx, e := db.Begin()
		if e != nil {
			return fail(e)
		}
		_, e = tx.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE devices (id TEXT PRIMARY KEY, payload TEXT NOT NULL);
CREATE TABLE tunnels (id TEXT PRIMARY KEY, payload TEXT NOT NULL);
CREATE TABLE history (id TEXT PRIMARY KEY, device_id TEXT NOT NULL, tunnel_id TEXT NOT NULL, protocol TEXT NOT NULL, source TEXT NOT NULL, started TEXT NOT NULL, ended TEXT, payload TEXT NOT NULL);
CREATE INDEX history_time ON history(started DESC);
CREATE INDEX history_end ON history(ended);
CREATE INDEX history_tunnel ON history(tunnel_id, started DESC);
CREATE INDEX history_device ON history(device_id, started DESC);
CREATE TABLE upgrades (id TEXT PRIMARY KEY, payload TEXT NOT NULL);
PRAGMA user_version=1;`)
		if e != nil {
			tx.Rollback()
			return fail(e)
		}
		if e = tx.Commit(); e != nil {
			return fail(e)
		}
	}
	rows, err := db.Query("SELECT payload FROM history WHERE ended IS NULL")
	if err != nil {
		return fail(err)
	}
	var stale []Connection
	for rows.Next() {
		var b []byte
		var c Connection
		if err = rows.Scan(&b); err != nil {
			break
		}
		if err = json.Unmarshal(b, &c); err != nil {
			break
		}
		stale = append(stale, c)
	}
	if e := rows.Err(); err == nil {
		err = e
	}
	rows.Close()
	if err != nil {
		return fail(err)
	}
	for _, c := range stale {
		now := time.Now().UTC()
		c.Ended = &now
		c.Result = "interrupted"
		c.Reason = "服务端异常退出，结束时间为恢复检测时间，流量可能不完整"
		if err = s.SaveConnections([]Connection{c}); err != nil {
			return fail(err)
		}
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) Get(key string) (string, error) {
	var v string
	err := s.DB.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}
func (s *Store) Set(key, value string) error {
	_, err := s.DB.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}
func (s *Store) Put(table, id string, v any) error {
	if table != "devices" && table != "tunnels" && table != "upgrades" {
		return errors.New("存储资源无效")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("INSERT INTO "+table+"(id,payload) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", id, string(b))
	return err
}
func (s *Store) All(table string, v any) error {
	if table != "devices" && table != "tunnels" && table != "upgrades" {
		return errors.New("存储资源无效")
	}
	rows, err := s.DB.Query("SELECT payload FROM " + table + " ORDER BY id")
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			return err
		}
		items = append(items, b)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	b, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func (s *Store) DeleteTunnel(id string) error {
	_, err := s.DB.Exec("DELETE FROM tunnels WHERE id=?", id)
	return err
}
func (s *Store) SaveConnections(items []Connection) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range items {
		b, e := json.Marshal(c)
		if e != nil {
			return e
		}
		var end any
		if c.Ended != nil {
			end = c.Ended.UTC().Format(time.RFC3339Nano)
		}
		_, e = tx.Exec(`INSERT INTO history(id,device_id,tunnel_id,protocol,source,started,ended,payload) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET ended=excluded.ended,payload=excluded.payload`, c.ID, c.DeviceID, c.TunnelID, c.Protocol, c.Source, c.Started.UTC().Format(time.RFC3339Nano), end, string(b))
		if e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) History(f Filter) ([]Connection, int, error) {
	where := []string{"1=1"}
	args := []any{}
	for _, p := range []struct{ k, v string }{{"device_id", f.DeviceID}, {"tunnel_id", f.TunnelID}, {"protocol", f.Protocol}} {
		if p.v != "" {
			where = append(where, p.k+"=?")
			args = append(args, p.v)
		}
	}
	if f.Source != "" {
		where = append(where, `source LIKE ? ESCAPE '\'`)
		escaped := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(f.Source)
		args = append(args, "%"+escaped+"%")
	}
	if f.From != "" {
		where = append(where, "started>=?")
		args = append(args, f.From)
	}
	if f.To != "" {
		where = append(where, "started<=?")
		args = append(args, f.To)
	}
	if f.State == "current" {
		where = append(where, "ended IS NULL")
	} else if f.State == "history" {
		where = append(where, "ended IS NOT NULL")
	}
	w := strings.Join(where, " AND ")
	var count int
	if err := s.DB.QueryRow("SELECT count(*) FROM history WHERE "+w, args...).Scan(&count); err != nil {
		return nil, 0, err
	}
	args = append(args, f.Limit, (f.Page-1)*f.Limit)
	rows, err := s.DB.Query("SELECT payload FROM history WHERE "+w+" ORDER BY started DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []Connection{}
	for rows.Next() {
		var b []byte
		var c Connection
		if err = rows.Scan(&b); err != nil {
			return nil, 0, err
		}
		if err = json.Unmarshal(b, &c); err != nil {
			return nil, 0, err
		}
		items = append(items, c)
	}
	return items, count, rows.Err()
}
func (s *Store) Prune(now time.Time) error {
	_, err := s.DB.Exec("DELETE FROM history WHERE id IN (SELECT id FROM history WHERE ended IS NOT NULL AND ended<? LIMIT 1000)", now.Add(-7*24*time.Hour).UTC().Format(time.RFC3339Nano))
	return err
}
func (c Connection) String() string {
	return fmt.Sprintf("%s %s → %s", c.Protocol, c.Source, c.Target)
}
