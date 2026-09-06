// Package dns is the panel's record of the zones this host serves.
//
// The rows here describe intent; the zone files named reads are what actually
// answer queries, and the Agent owns those. The relationship is one-way and is
// the same arrangement the cron and FTP packages use: after every change the
// panel hands the Agent the *complete* set of zones and the Agent makes the
// host match. Nothing merges, nothing is incremental, and a zone file therefore
// cannot drift into saying something the database does not.
//
// Two things are deliberately not stored. DNSSEC private keys, which are
// named's and would be in every backup of this database if they were here; and
// the serial named is actually serving, which with inline signing diverges from
// the one the panel wrote within seconds of a reload — see the note on Serial.
package dns

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors returned by the repository.
var (
	ErrZoneNotFound   = errors.New("DNS zone not found")
	ErrRecordNotFound = errors.New("DNS record not found")
	// ErrDuplicateZone means this host already serves that name. Two zones of
	// one name is a configuration named refuses to load.
	ErrDuplicateZone = errors.New("this server already serves that zone")
	// ErrDuplicateRecord means the same name, type and value is already there.
	// named would load the duplicate and serve it once, so the second row would
	// be invisible except as a row nobody can account for.
	ErrDuplicateRecord = errors.New("that record is already in this zone")
	// ErrProviderNotFound means no remote provider with that id.
	ErrProviderNotFound = errors.New("DNS provider not found")
)

// Zone is one zone as the panel records it.
type Zone struct {
	ID        string `json:"id"`
	ServerID  string `json:"server_id"`
	WebsiteID string `json:"website_id,omitempty"`

	Name string `json:"name"`
	Kind string `json:"kind"`
	// ReverseNetwork is the network a reverse zone covers, empty for a forward
	// zone. It is kept because a PTR's owner name cannot be checked against the
	// zone without it.
	ReverseNetwork string `json:"reverse_network,omitempty"`

	PrimaryNS  string `json:"primary_ns"`
	Hostmaster string `json:"hostmaster"`
	// Serial is the panel's own, written into the zone file.
	//
	// It is not the serial the server is answering with. With inline signing
	// named maintains a second one on the signed copy, and it runs ahead — a
	// file at 2026090302 was served as 2026090304 seconds after loading. The
	// served serial is reported separately, by asking the host.
	Serial  int64 `json:"serial"`
	Refresh int   `json:"refresh"`
	Retry   int   `json:"retry"`
	Expire  int   `json:"expire"`
	Minimum int   `json:"minimum"`
	TTL     int   `json:"ttl"`

	Nameservers []string `json:"nameservers"`
	DNSSEC      bool     `json:"dnssec"`

	AllowTransfer []string `json:"allow_transfer"`
	AlsoNotify    []string `json:"also_notify"`
	Masters       []string `json:"masters"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// WebsiteDomain is joined for the page to show.
	WebsiteDomain string `json:"website_domain,omitempty"`
	// RecordCount is filled in by listing queries.
	RecordCount int `json:"record_count"`

	// Records are loaded on demand, by the zone editor.
	Records []Record `json:"records,omitempty"`
}

// Record is one resource record.
type Record struct {
	ID     string `json:"id"`
	ZoneID string `json:"zone_id"`

	Name     string `json:"name"`
	Type     string `json:"type"`
	TTL      int    `json:"ttl"`
	Value    string `json:"value"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Port     int    `json:"port"`
	Flags    int    `json:"flags"`
	Tag      string `json:"tag"`

	Provider   string `json:"provider"`
	ExternalID string `json:"external_id,omitempty"`
	// Managed marks a record the panel writes for itself — the NS and A records
	// a subdomain needs in its parent's zone. It is shown and not editable:
	// editing one by hand would leave the panel and the zone disagreeing about
	// a name the panel is responsible for.
	Managed bool `json:"managed"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Settings are the name server's options for one host.
type Settings struct {
	ServerID      string   `json:"server_id"`
	ListenOn      []string `json:"listen_on"`
	AllowTransfer []string `json:"allow_transfer"`
	DNSSECPolicy  string   `json:"dnssec_policy"`

	// The defaults a new zone is created with, so an operator sets their name
	// servers once rather than on every zone.
	DefaultNS  []string `json:"default_ns"`
	DefaultTTL int      `json:"default_ttl"`
	Hostmaster string   `json:"hostmaster"`
}

// DefaultSettings are what a host has before anybody has chosen.
func DefaultSettings(serverID string) Settings {
	return Settings{
		ServerID:      serverID,
		ListenOn:      []string{},
		AllowTransfer: []string{},
		DNSSECPolicy:  "default",
		DefaultNS:     []string{},
		DefaultTTL:    3600,
	}
}

// Provider is a remote DNS service the panel can publish to.
//
// The token is never in this struct. It is read out of the database only at the
// moment a request is made to the provider, decrypted into a local variable,
// and not returned by anything the API serves.
type Provider struct {
	ID        string `json:"id"`
	ServerID  string `json:"server_id"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	AccountID string `json:"account_id,omitempty"`

	LastSyncAt     *time.Time `json:"last_sync_at,omitempty"`
	LastSyncStatus string     `json:"last_sync_status,omitempty"`
	LastSyncError  string     `json:"last_sync_error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
}

// Encrypter seals a value against the row it belongs to.
//
// The context binds a ciphertext to its row, which is what stops a token copied
// from one provider row into another from decrypting: the same arrangement the
// databases and node packages use for their secrets.
type Encrypter interface {
	Encrypt(plaintext []byte, context string) (string, error)
	Decrypt(encoded, context string) ([]byte, error)
}

// Repository reads and writes DNS records.
type Repository struct {
	pool      *pgxpool.Pool
	encrypter Encrypter
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool, encrypter Encrypter) *Repository {
	return &Repository{pool: pool, encrypter: encrypter}
}

// tokenContext binds a provider's token to its row.
func tokenContext(providerID string) string { return "dns_provider:" + providerID }

// zoneColumns is qualified with the table's alias.
//
// Every query aliases dns_zones as z, and the columns say so: the listing joins
// websites, which also has an id and a created_at, and unqualified names there
// are ambiguous rather than wrong — which PostgreSQL reports at run time, not
// at compile time.
const zoneColumns = `
	z.id, z.server_id, COALESCE(z.website_id::text, ''), z.name, z.kind,
	COALESCE(host(z.reverse_network) || '/' || masklen(z.reverse_network), ''),
	z.primary_ns, z.hostmaster, z.serial, z.refresh, z.retry, z.expire,
	z.minimum, z.ttl, z.nameservers, z.dnssec, z.allow_transfer, z.also_notify,
	z.masters, z.created_at, z.updated_at`

// returningZoneColumns is the same list for a RETURNING clause, which has no
// table alias to qualify with.
const returningZoneColumns = `
	id, server_id, COALESCE(website_id::text, ''), name, kind,
	COALESCE(host(reverse_network) || '/' || masklen(reverse_network), ''),
	primary_ns, hostmaster, serial, refresh, retry, expire, minimum, ttl,
	nameservers, dnssec, allow_transfer, also_notify, masters,
	created_at, updated_at`

// scanZone reads a zone row.
func scanZone(row pgx.Row) (Zone, error) {
	var zone Zone
	err := row.Scan(
		&zone.ID, &zone.ServerID, &zone.WebsiteID, &zone.Name, &zone.Kind,
		&zone.ReverseNetwork,
		&zone.PrimaryNS, &zone.Hostmaster, &zone.Serial, &zone.Refresh,
		&zone.Retry, &zone.Expire, &zone.Minimum, &zone.TTL,
		&zone.Nameservers, &zone.DNSSEC, &zone.AllowTransfer, &zone.AlsoNotify,
		&zone.Masters, &zone.CreatedAt, &zone.UpdatedAt)
	if err != nil {
		return Zone{}, err
	}
	return zone, nil
}

// CreateZoneParams describe a zone to record.
type CreateZoneParams struct {
	ServerID       string
	WebsiteID      string
	Name           string
	Kind           string
	ReverseNetwork string
	PrimaryNS      string
	Hostmaster     string
	Refresh        int
	Retry          int
	Expire         int
	Minimum        int
	TTL            int
	Nameservers    []string
	DNSSEC         bool
	AllowTransfer  []string
	AlsoNotify     []string
	Masters        []string
}

// CreateZone writes a zone row.
func (r *Repository) CreateZone(ctx context.Context, params CreateZoneParams) (Zone, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO dns_zones
			(server_id, website_id, name, kind, reverse_network, primary_ns,
			 hostmaster, serial, refresh, retry, expire, minimum, ttl,
			 nameservers, dnssec, allow_transfer, also_notify, masters)
		VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4, NULLIF($5, '')::cidr, $6,
			$7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		RETURNING `+returningZoneColumns,
		params.ServerID, params.WebsiteID, params.Name, params.Kind,
		params.ReverseNetwork, params.PrimaryNS, params.Hostmaster,
		// The first serial is the current time, for the reason the migration
		// gives: monotonic, and it cannot run out within a day.
		time.Now().Unix(),
		params.Refresh, params.Retry, params.Expire, params.Minimum, params.TTL,
		params.Nameservers, params.DNSSEC, params.AllowTransfer,
		params.AlsoNotify, params.Masters)

	zone, err := scanZone(row)
	if err != nil {
		return Zone{}, translateConstraint(err)
	}
	return zone, nil
}

// UpdateZoneParams describe a change to a zone.
//
// The name cannot change. A zone under a different name is a different zone —
// every record in it is relative to the apex — and renaming one would mean
// deleting a zone the internet is being told about and creating another.
type UpdateZoneParams struct {
	PrimaryNS     *string
	Hostmaster    *string
	Refresh       *int
	Retry         *int
	Expire        *int
	Minimum       *int
	TTL           *int
	Nameservers   *[]string
	DNSSEC        *bool
	AllowTransfer *[]string
	AlsoNotify    *[]string
	Masters       *[]string
	WebsiteID     *string
}

// UpdateZone applies changes and bumps the serial.
func (r *Repository) UpdateZone(ctx context.Context, id string, params UpdateZoneParams) (Zone, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE dns_zones SET
			primary_ns     = COALESCE($2, primary_ns),
			hostmaster     = COALESCE($3, hostmaster),
			refresh        = COALESCE($4, refresh),
			retry          = COALESCE($5, retry),
			expire         = COALESCE($6, expire),
			minimum        = COALESCE($7, minimum),
			ttl            = COALESCE($8, ttl),
			nameservers    = COALESCE($9, nameservers),
			dnssec         = COALESCE($10, dnssec),
			allow_transfer = COALESCE($11, allow_transfer),
			also_notify    = COALESCE($12, also_notify),
			masters        = COALESCE($13, masters),
			website_id     = CASE WHEN $14::text IS NULL THEN website_id
			                      ELSE NULLIF($14, '')::uuid END,
			serial         = `+nextSerial+`,
			updated_at     = now()
		WHERE id = $1::uuid
		RETURNING `+returningZoneColumns,
		id, params.PrimaryNS, params.Hostmaster, params.Refresh, params.Retry,
		params.Expire, params.Minimum, params.TTL, params.Nameservers,
		params.DNSSEC, params.AllowTransfer, params.AlsoNotify, params.Masters,
		params.WebsiteID)

	zone, err := scanZone(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Zone{}, ErrZoneNotFound
		}
		return Zone{}, translateConstraint(err)
	}
	return zone, nil
}

// nextSerial is how a zone's serial advances.
//
// The greater of "one more than the current one" and the current unix time. The
// time keeps it meaningful to a human reading a dig output and monotonic across
// restores; the increment covers two changes in the same second, which would
// otherwise produce the same serial twice and leave every secondary serving the
// older zone until the next change.
const nextSerial = `GREATEST(serial + 1, EXTRACT(EPOCH FROM now())::bigint)`

// BumpSerial advances a zone's serial without changing anything else.
//
// Used when a record changed rather than the zone: the file is rewritten, so
// the serial has to move, or no secondary will ever pull the new contents.
func (r *Repository) BumpSerial(ctx context.Context, zoneID string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE dns_zones SET serial = `+nextSerial+`, updated_at = now()
		WHERE id = $1::uuid`, zoneID)
	if err != nil {
		return fmt.Errorf("bump the zone serial: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrZoneNotFound
	}
	return nil
}

// GetZone returns one zone, without its records.
func (r *Repository) GetZone(ctx context.Context, id string) (Zone, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+zoneColumns+` FROM dns_zones z WHERE z.id = $1::uuid`, id)
	zone, err := scanZone(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Zone{}, ErrZoneNotFound
		}
		return Zone{}, fmt.Errorf("select DNS zone: %w", err)
	}
	return zone, nil
}

// ZoneByName returns a zone by its apex.
func (r *Repository) ZoneByName(ctx context.Context, serverID, name string) (Zone, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+zoneColumns+` FROM dns_zones z WHERE z.server_id = $1::uuid AND z.name = $2`,
		serverID, name)
	zone, err := scanZone(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Zone{}, ErrZoneNotFound
		}
		return Zone{}, fmt.Errorf("select DNS zone: %w", err)
	}
	return zone, nil
}

// ListZones returns every zone on a host, with its record count.
//
// Ordered by name because this is also the set handed to the Agent, and a
// stable order makes two reconciles of an unchanged panel produce byte-identical
// configuration — which is what lets the Agent leave an unchanged zone alone
// rather than rewriting, reloading and re-signing it.
func (r *Repository) ListZones(ctx context.Context, serverID string) ([]Zone, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+zoneColumns+`,
			COALESCE(w.primary_domain, ''),
			(SELECT count(*) FROM dns_records rec WHERE rec.zone_id = z.id)
		FROM dns_zones z
		LEFT JOIN websites w ON w.id = z.website_id
		WHERE z.server_id = $1::uuid
		ORDER BY z.name`, serverID)
	if err != nil {
		return nil, fmt.Errorf("list DNS zones: %w", err)
	}
	defer rows.Close()

	zones := make([]Zone, 0, 8)
	for rows.Next() {
		var zone Zone
		if err := rows.Scan(
			&zone.ID, &zone.ServerID, &zone.WebsiteID, &zone.Name, &zone.Kind,
			&zone.ReverseNetwork, &zone.PrimaryNS, &zone.Hostmaster, &zone.Serial,
			&zone.Refresh, &zone.Retry, &zone.Expire, &zone.Minimum, &zone.TTL,
			&zone.Nameservers, &zone.DNSSEC, &zone.AllowTransfer, &zone.AlsoNotify,
			&zone.Masters, &zone.CreatedAt, &zone.UpdatedAt,
			&zone.WebsiteDomain, &zone.RecordCount,
		); err != nil {
			return nil, fmt.Errorf("scan DNS zone: %w", err)
		}
		zones = append(zones, zone)
	}
	return zones, rows.Err()
}

// DeleteZone removes a zone and its records.
func (r *Repository) DeleteZone(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM dns_zones WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete DNS zone: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrZoneNotFound
	}
	return nil
}

const recordColumns = `
	id, zone_id, name, type, ttl, value, priority, weight, port, flags, tag,
	provider, external_id, managed, created_at, updated_at`

// scanRecord reads a record row.
func scanRecord(row pgx.Row) (Record, error) {
	var record Record
	err := row.Scan(
		&record.ID, &record.ZoneID, &record.Name, &record.Type, &record.TTL,
		&record.Value, &record.Priority, &record.Weight, &record.Port,
		&record.Flags, &record.Tag, &record.Provider, &record.ExternalID,
		&record.Managed, &record.CreatedAt, &record.UpdatedAt)
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

// RecordParams describe a record to write.
type RecordParams struct {
	ZoneID   string
	Name     string
	Type     string
	TTL      int
	Value    string
	Priority int
	Weight   int
	Port     int
	Flags    int
	Tag      string
	Managed  bool
}

// CreateRecord writes a record row.
func (r *Repository) CreateRecord(ctx context.Context, params RecordParams) (Record, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO dns_records
			(zone_id, name, type, ttl, value, priority, weight, port, flags, tag, managed)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+recordColumns,
		params.ZoneID, params.Name, params.Type, params.TTL, params.Value,
		params.Priority, params.Weight, params.Port, params.Flags, params.Tag,
		params.Managed)

	record, err := scanRecord(row)
	if err != nil {
		return Record{}, translateConstraint(err)
	}
	return record, nil
}

// UpdateRecord replaces a record's contents.
func (r *Repository) UpdateRecord(ctx context.Context, id string, params RecordParams) (Record, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE dns_records SET
			name = $2, type = $3, ttl = $4, value = $5, priority = $6,
			weight = $7, port = $8, flags = $9, tag = $10, updated_at = now()
		WHERE id = $1::uuid
		RETURNING `+recordColumns,
		id, params.Name, params.Type, params.TTL, params.Value,
		params.Priority, params.Weight, params.Port, params.Flags, params.Tag)

	record, err := scanRecord(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Record{}, ErrRecordNotFound
		}
		return Record{}, translateConstraint(err)
	}
	return record, nil
}

// GetRecord returns one record.
func (r *Repository) GetRecord(ctx context.Context, id string) (Record, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+recordColumns+` FROM dns_records WHERE id = $1::uuid`, id)
	record, err := scanRecord(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Record{}, ErrRecordNotFound
		}
		return Record{}, fmt.Errorf("select DNS record: %w", err)
	}
	return record, nil
}

// ListRecords returns a zone's records.
func (r *Repository) ListRecords(ctx context.Context, zoneID string) ([]Record, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+recordColumns+` FROM dns_records WHERE zone_id = $1::uuid
		 ORDER BY name, type, priority, value`, zoneID)
	if err != nil {
		return nil, fmt.Errorf("list DNS records: %w", err)
	}
	defer rows.Close()

	records := make([]Record, 0, 16)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan DNS record: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

// DeleteRecord removes a record.
func (r *Repository) DeleteRecord(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM dns_records WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete DNS record: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRecordNotFound
	}
	return nil
}

// DeleteManagedRecords removes the records the panel wrote for a name.
//
// Used when a subdomain is deleted: the NS and A records the panel put in the
// parent's zone go with it. Only the managed ones, so a record an operator
// added at the same name — a TXT for a service, say — survives the subdomain
// being removed.
func (r *Repository) DeleteManagedRecords(ctx context.Context, zoneID, name string) (int, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM dns_records WHERE zone_id = $1::uuid AND name = $2 AND managed`,
		zoneID, name)
	if err != nil {
		return 0, fmt.Errorf("delete managed DNS records: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// DeleteUnmanagedRecords clears a zone's records for an import that replaces
// them.
//
// Managed records are kept. They are the NS and A records the panel writes for
// its own subdomains, and they are regenerated from the panel's own records
// rather than typed — so a provider's copy has no opinion about them worth
// acting on, and removing them here would break the delegation the panel
// maintains.
func (r *Repository) DeleteUnmanagedRecords(ctx context.Context, zoneID string) (int, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM dns_records WHERE zone_id = $1::uuid AND NOT managed`, zoneID)
	if err != nil {
		return 0, fmt.Errorf("clear DNS records for an import: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Settings returns a host's name server settings, or the defaults.
func (r *Repository) Settings(ctx context.Context, serverID string) (Settings, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT server_id, listen_on, allow_transfer, dnssec_policy,
		       default_ns, default_ttl, hostmaster
		FROM dns_settings WHERE server_id = $1::uuid`, serverID)

	var settings Settings
	err := row.Scan(&settings.ServerID, &settings.ListenOn, &settings.AllowTransfer,
		&settings.DNSSECPolicy, &settings.DefaultNS, &settings.DefaultTTL,
		&settings.Hostmaster)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DefaultSettings(serverID), nil
		}
		return Settings{}, fmt.Errorf("select DNS settings: %w", err)
	}
	return settings, nil
}

// SaveSettings writes a host's settings.
func (r *Repository) SaveSettings(ctx context.Context, settings Settings) (Settings, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO dns_settings
			(server_id, listen_on, allow_transfer, dnssec_policy, default_ns,
			 default_ttl, hostmaster)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (server_id) DO UPDATE SET
			listen_on      = EXCLUDED.listen_on,
			allow_transfer = EXCLUDED.allow_transfer,
			dnssec_policy  = EXCLUDED.dnssec_policy,
			default_ns     = EXCLUDED.default_ns,
			default_ttl    = EXCLUDED.default_ttl,
			hostmaster     = EXCLUDED.hostmaster,
			updated_at     = now()
		RETURNING server_id, listen_on, allow_transfer, dnssec_policy,
		          default_ns, default_ttl, hostmaster`,
		settings.ServerID, settings.ListenOn, settings.AllowTransfer,
		settings.DNSSECPolicy, settings.DefaultNS, settings.DefaultTTL,
		settings.Hostmaster)

	var saved Settings
	if err := row.Scan(&saved.ServerID, &saved.ListenOn, &saved.AllowTransfer,
		&saved.DNSSECPolicy, &saved.DefaultNS, &saved.DefaultTTL,
		&saved.Hostmaster); err != nil {
		return Settings{}, translateConstraint(err)
	}
	return saved, nil
}

// CreateProvider records a remote DNS provider's credentials.
//
// Two statements in one transaction, because the token is sealed against the
// row's own id and the id does not exist until the insert has run. A failure
// between them would leave a provider row with no usable token, so they commit
// together or not at all.
func (r *Repository) CreateProvider(ctx context.Context, serverID, kind, label,
	token, accountID string,
) (Provider, error) {
	if r.encrypter == nil {
		return Provider{}, errors.New(
			"this panel has no encryption key configured, so a provider token cannot be stored")
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Provider{}, fmt.Errorf("record the DNS provider: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
		INSERT INTO dns_providers (server_id, kind, label, api_token_encrypted, account_id)
		VALUES ($1::uuid, $2, $3, '', $4)
		RETURNING id, server_id, kind, label, account_id, last_sync_at,
		          last_sync_status, last_sync_error, created_at`,
		serverID, kind, label, accountID)
	provider, err := scanProvider(row)
	if err != nil {
		return Provider{}, translateConstraint(err)
	}

	sealed, err := r.encrypter.Encrypt([]byte(token), tokenContext(provider.ID))
	if err != nil {
		return Provider{}, fmt.Errorf("encrypt the provider token: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE dns_providers SET api_token_encrypted = $2 WHERE id = $1::uuid`,
		provider.ID, sealed); err != nil {
		return Provider{}, fmt.Errorf("store the provider token: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Provider{}, fmt.Errorf("record the DNS provider: %w", err)
	}
	return provider, nil
}

// scanProvider reads a provider row. The token is never selected.
func scanProvider(row pgx.Row) (Provider, error) {
	var provider Provider
	err := row.Scan(&provider.ID, &provider.ServerID, &provider.Kind, &provider.Label,
		&provider.AccountID, &provider.LastSyncAt, &provider.LastSyncStatus,
		&provider.LastSyncError, &provider.CreatedAt)
	if err != nil {
		return Provider{}, err
	}
	return provider, nil
}

// ListProviders returns a host's remote providers.
func (r *Repository) ListProviders(ctx context.Context, serverID string) ([]Provider, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, server_id, kind, label, account_id, last_sync_at,
		       last_sync_status, last_sync_error, created_at
		FROM dns_providers WHERE server_id = $1::uuid ORDER BY created_at`, serverID)
	if err != nil {
		return nil, fmt.Errorf("list DNS providers: %w", err)
	}
	defer rows.Close()

	providers := make([]Provider, 0, 4)
	for rows.Next() {
		provider, err := scanProvider(rows)
		if err != nil {
			return nil, fmt.Errorf("scan DNS provider: %w", err)
		}
		providers = append(providers, provider)
	}
	return providers, rows.Err()
}

// ProviderToken returns a provider's kind and its decrypted token.
//
// Its own method, called only where a request to the provider is about to be
// made. Keeping it out of the row-reading path is what makes it impossible for
// a token to be returned by an endpoint that happens to serialise a Provider.
func (r *Repository) ProviderToken(ctx context.Context, id string) (string, string, error) {
	if r.encrypter == nil {
		return "", "", errors.New("this panel has no encryption key, so a stored token cannot be read")
	}

	row := r.pool.QueryRow(ctx,
		`SELECT kind, api_token_encrypted FROM dns_providers WHERE id = $1::uuid`, id)
	var kind, sealed string
	if err := row.Scan(&kind, &sealed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", ErrProviderNotFound
		}
		return "", "", fmt.Errorf("select DNS provider: %w", err)
	}

	token, err := r.encrypter.Decrypt(sealed, tokenContext(id))
	if err != nil {
		// The usual cause is a changed ENCRYPTION_KEY, and saying so is more
		// use than "decryption failed": the fix is to enter the token again.
		return "", "", fmt.Errorf(
			"the stored token for this provider could not be decrypted; if the panel's "+
				"encryption key has changed, the token has to be entered again: %w", err)
	}
	return kind, string(token), nil
}

// DeleteProvider removes a provider's credentials.
func (r *Repository) DeleteProvider(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM dns_providers WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete DNS provider: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrProviderNotFound
	}
	return nil
}

// RecordSync stores what a sync did.
func (r *Repository) RecordSync(ctx context.Context, providerID, status, message string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE dns_providers
		SET last_sync_at = now(), last_sync_status = $2, last_sync_error = $3,
		    updated_at = now()
		WHERE id = $1::uuid`, providerID, status, message)
	if err != nil {
		return fmt.Errorf("record the DNS sync: %w", err)
	}
	return nil
}

// translateConstraint turns a database constraint into an error the API can
// explain.
//
// The messages say what the rule is for, because a constraint name in an error
// body tells an operator nothing they can act on.
func translateConstraint(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "dns_zones_server_name_key"):
		return ErrDuplicateZone
	case strings.Contains(message, "dns_records_unique"):
		return ErrDuplicateRecord
	case strings.Contains(message, "dns_zones_timers_sane"):
		return errors.New("the SOA timers are outside the range this panel writes")
	case strings.Contains(message, "dns_zones_slave_has_masters"):
		return errors.New("a secondary zone must name at least one primary to transfer from")
	case strings.Contains(message, "dns_records_srv_has_port"):
		return errors.New("an SRV record needs a port")
	case strings.Contains(message, "dns_records_caa_tag"):
		return errors.New("a CAA tag is issue, issuewild or iodef")
	case strings.Contains(message, "dns_records_name_format"):
		return errors.New("that record name could not be written into a zone file")
	case strings.Contains(message, "dns_zones_name_format"):
		return errors.New("that zone name is not a domain name")
	default:
		return fmt.Errorf("write DNS record: %w", err)
	}
}
