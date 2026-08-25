// Package store persists the Cloudflare mirror in MariaDB. Cloudflare is the
// single source of truth; these tables are a denormalized copy keyed by the
// Cloudflare record ID. All SQL is parameterized.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"sort"

	_ "github.com/go-sql-driver/mysql"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const (
	StatusOn  = "on"
	StatusOff = "off"
	// StateKeyLastIP tracks the last public IP the mirror was synced to.
	StateKeyLastIP = "last_known_ip"
)

// Zone mirrors one Cloudflare zone (domain).
type Zone struct {
	ID        int64
	Name      string
	ZoneID    string
	Registrar string
	Status    string
}

// Record mirrors one Cloudflare DNS record.
type Record struct {
	ID       int64
	DomainID int64
	Type     string
	Name     string
	Content  string
	Proxied  bool
	TTL      int
	Priority int
	Status   string
	RecordID string
}

// HomeRecord is a DNS record joined with its zone's Cloudflare zone ID,
// used for IP updates so we can address the record directly in Cloudflare.
type HomeRecord struct {
	Record
	ZoneID string
}

// Store owns the database handle.
type Store struct {
	db  *sql.DB
	log *slog.Logger
}

// Open connects, pings, and applies pending migrations.
func Open(ctx context.Context, dsn string, log *slog.Logger) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	s := &Store{db: db, log: log}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying connection pool.
func (s *Store) Close() error { return s.db.Close() }

// migrate applies embedded migrations in order, recording each in
// schema_migrations.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		id varchar(255) NOT NULL PRIMARY KEY,
		applied_at timestamp NOT NULL DEFAULT current_timestamp()
	) ENGINE=InnoDB`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("list applied migrations: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan migration row: %w", err)
		}
		applied[id] = true
	}
	rows.Close()

	files, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		if applied[name] {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := s.db.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_migrations (id) VALUES (?)`, name); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		s.log.Info("migration applied", "migration", name)
	}
	return nil
}

// --- zones ---

// ListZones returns all zone rows keyed by Cloudflare zone ID.
func (s *Store) ListZones(ctx context.Context) (map[string]Zone, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT domainID, domainname, registrar, zoneid, status FROM domain`)
	if err != nil {
		return nil, fmt.Errorf("list zones: %w", err)
	}
	defer rows.Close()
	out := map[string]Zone{}
	for rows.Next() {
		var z Zone
		if err := rows.Scan(&z.ID, &z.Name, &z.Registrar, &z.ZoneID, &z.Status); err != nil {
			return nil, fmt.Errorf("scan zone: %w", err)
		}
		out[z.ZoneID] = z
	}
	return out, rows.Err()
}

// ListZonesForIP returns active zone rows with a non-empty zone ID and DNS
// records (mirror of the old dnsRecords view), used by update-ip.
func (s *Store) ListHomeARecords(ctx context.Context, content string) ([]HomeRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.dnsid, d.domain_id, d.type, d.name, d.content, d.proxied,
		       d.ttl, d.priority, d.status, d.recordid, dm.zoneid
		FROM dns d JOIN domain dm ON dm.domainID = d.domain_id
		WHERE d.type = 'A' AND d.status = 'on' AND dm.status = 'on'
		  AND dm.zoneid <> '' AND d.recordid <> '' AND d.content = ?`, content)
	if err != nil {
		return nil, fmt.Errorf("list home A records: %w", err)
	}
	defer rows.Close()
	var out []HomeRecord
	for rows.Next() {
		var r HomeRecord
		var proxied int
		var ttl sql.NullInt64
		if err := rows.Scan(&r.ID, &r.DomainID, &r.Type, &r.Name, &r.Content,
			&proxied, &ttl, &r.Priority, &r.Status, &r.RecordID, &r.ZoneID); err != nil {
			return nil, fmt.Errorf("scan home record: %w", err)
		}
		r.Proxied = proxied != 0
		if ttl.Valid {
			r.TTL = int(ttl.Int64)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertZone adds a new zone row. The registrar annotation defaults to
// 'cloudflare' (domain registration can live elsewhere; DNS is on CF).
func (s *Store) InsertZone(ctx context.Context, name, zoneID string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO domain (domainname, registrar, zoneid, status) VALUES (?, 'cloudflare', ?, 'on')`,
		name, zoneID)
	if err != nil {
		return 0, fmt.Errorf("insert zone %s: %w", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert zone %s lastID: %w", name, err)
	}
	return id, nil
}

// SetZoneID attaches a Cloudflare zone ID to an existing row (matched by name).
func (s *Store) SetZoneID(ctx context.Context, domainID int64, zoneID string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE domain SET zoneid = ?, status = 'on' WHERE domainID = ?`, zoneID, domainID); err != nil {
		return fmt.Errorf("set zoneid for domain %d: %w", domainID, err)
	}
	return nil
}

// RenameZone updates the zone's canonical name.
func (s *Store) RenameZone(ctx context.Context, domainID int64, name string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE domain SET domainname = ? WHERE domainID = ?`, name, domainID); err != nil {
		return fmt.Errorf("rename domain %d: %w", domainID, err)
	}
	return nil
}

// SetZoneStatus flips a zone's status.
func (s *Store) SetZoneStatus(ctx context.Context, domainID int64, status string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE domain SET status = ? WHERE domainID = ?`, status, domainID); err != nil {
		return fmt.Errorf("set zone status domain %d: %w", domainID, err)
	}
	return nil
}

// --- records ---

// ListRecords returns all records of a domain.
func (s *Store) ListRecords(ctx context.Context, domainID int64) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT dnsid, domain_id, type, name, content, proxied, ttl, priority, status, recordid
		FROM dns WHERE domain_id = ?`, domainID)
	if err != nil {
		return nil, fmt.Errorf("list records domain %d: %w", domainID, err)
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var proxied int
		var ttl sql.NullInt64
		if err := rows.Scan(&r.ID, &r.DomainID, &r.Type, &r.Name, &r.Content,
			&proxied, &ttl, &r.Priority, &r.Status, &r.RecordID); err != nil {
			return nil, fmt.Errorf("scan record: %w", err)
		}
		r.Proxied = proxied != 0
		if ttl.Valid {
			r.TTL = int(ttl.Int64)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertRecord adds a mirror row. recordid is the Cloudflare record ID.
func (s *Store) InsertRecord(ctx context.Context, domainID int64, r Record) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO dns (domain_id, type, name, content, proxied, ttl, priority, status, recordid)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'on', ?)`,
		domainID, r.Type, r.Name, r.Content, boolToInt(r.Proxied), r.TTL, r.Priority, r.RecordID)
	if err != nil {
		return 0, fmt.Errorf("insert record %s %s: %w", r.Type, r.Name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("insert record lastID: %w", err)
	}
	return id, nil
}

// UpdateRecord overwrites the mirror fields of an existing row. recordid is
// set so rows that were previously matched by composite key get their stable
// Cloudflare identity.
func (s *Store) UpdateRecord(ctx context.Context, dnsID int64, r Record) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE dns SET type = ?, name = ?, content = ?, proxied = ?, ttl = ?,
		       priority = ?, status = 'on', recordid = ?
		WHERE dnsid = ?`,
		r.Type, r.Name, r.Content, boolToInt(r.Proxied), nullableTTL(r.TTL), r.Priority, r.RecordID, dnsID); err != nil {
		return fmt.Errorf("update record %d: %w", dnsID, err)
	}
	return nil
}

// SetRecordStatus flips a record mirror row's status.
func (s *Store) SetRecordStatus(ctx context.Context, dnsID int64, status string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE dns SET status = ? WHERE dnsid = ?`, status, dnsID); err != nil {
		return fmt.Errorf("set record status %d: %w", dnsID, err)
	}
	return nil
}

// UpdateRecordContent updates only the content of a record (post-IP-change).
func (s *Store) UpdateRecordContent(ctx context.Context, dnsID int64, content string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE dns SET content = ? WHERE dnsid = ?`, content, dnsID); err != nil {
		return fmt.Errorf("update record content %d: %w", dnsID, err)
	}
	return nil
}

// --- app state ---

// GetState reads a key from app_state. ok is false when the key is absent.
func (s *Store) GetState(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT v FROM app_state WHERE k = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get state %s: %w", key, err)
	}
	return v, true, nil
}

// SetState writes a key into app_state.
func (s *Store) SetState(ctx context.Context, key, value string) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO app_state (k, v) VALUES (?, ?)
		 ON DUPLICATE KEY UPDATE v = VALUES(v)`, key, value); err != nil {
		return fmt.Errorf("set state %s: %w", key, err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullableTTL maps a 0/absent TTL to NULL (legacy rows may hold NULL; mirror
// writes persist the numeric value Cloudflare returns, 1 meaning "auto").
func nullableTTL(ttl int) any {
	if ttl <= 0 {
		return nil
	}
	return ttl
}
