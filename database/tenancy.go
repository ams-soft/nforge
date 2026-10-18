package database

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ams-soft/nforge/database/internal/rlsexec"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
)

// Tenancy modes.
const (
	// TenancyRLS isolates tenants with PostgreSQL Row-Level Security: every
	// tenant-scoped statement runs in a transaction where the tenant setting
	// (app.tenant_id by default) is set with set_config(..., true), and the
	// application's policies filter rows by it.
	TenancyRLS = "rls"
	// TenancyColumn relies on the tenant column alone: nothing is set on the
	// connection and the query author adds the predicate (TenantEq).
	TenancyColumn = "column"
)

const (
	defaultTenantColumn  = "tenant_id"
	defaultTenantSchema  = "public"
	defaultTenantSetting = "app.tenant_id"
)

// TenancyConfig is the `database.tenancy:` section. The database objects
// (policies, columns, indexes) belong to the application's migrations; the
// framework only scopes connections and checks the listed tables at startup.
type TenancyConfig struct {
	Enabled bool     `yaml:"enabled"` // default: false
	Mode    string   `yaml:"mode"`    // rls | column; required when enabled
	Column  string   `yaml:"column"`  // tenant column used by TenantEq and the startup check; default: tenant_id
	Schema  string   `yaml:"schema"`  // schema of unqualified tables; default: public
	Setting string   `yaml:"setting"` // rls only: custom setting read by the policies; default: app.tenant_id
	Tables  []string `yaml:"tables"`  // "table" or "schema.table"; checked at startup; default: none
}

var (
	identRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	settingRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+$`)
)

// withDefaults returns a copy with the defaults applied and the mode
// lowercased; Tables is copied so the caller's slice is never shared.
func (c TenancyConfig) withDefaults() TenancyConfig {
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Column == "" {
		c.Column = defaultTenantColumn
	}
	if c.Schema == "" {
		c.Schema = defaultTenantSchema
	}
	if c.Setting == "" {
		c.Setting = defaultTenantSetting
	}
	c.Tables = append([]string(nil), c.Tables...)
	return c
}

// Validate checks the section after applying its defaults. A disabled
// section is always valid. Identifiers are restricted to plain unquoted SQL
// names because the column is written into the SQL built by TenantEq.
func (c TenancyConfig) Validate() error {
	if !c.Enabled {
		return nil
	}
	c = c.withDefaults()
	switch c.Mode {
	case TenancyRLS, TenancyColumn:
	case "":
		return fmt.Errorf("%w: database.tenancy.mode is required (rls or column)", ErrInvalidTenancy)
	default:
		return fmt.Errorf("%w: database.tenancy.mode must be rls or column, got %q", ErrInvalidTenancy, c.Mode)
	}
	if !identRe.MatchString(c.Column) {
		return fmt.Errorf("%w: database.tenancy.column %q is not a plain SQL identifier", ErrInvalidTenancy, c.Column)
	}
	if !identRe.MatchString(c.Schema) {
		return fmt.Errorf("%w: database.tenancy.schema %q is not a plain SQL identifier", ErrInvalidTenancy, c.Schema)
	}
	if c.Mode == TenancyRLS && !settingRe.MatchString(c.Setting) {
		return fmt.Errorf("%w: database.tenancy.setting %q must be a custom setting like app.tenant_id", ErrInvalidTenancy, c.Setting)
	}
	for _, t := range c.Tables {
		if _, _, err := c.splitTable(t); err != nil {
			return err
		}
	}
	return nil
}

// splitTable resolves "table" or "schema.table" against the configured schema.
func (c TenancyConfig) splitTable(name string) (schema, table string, err error) {
	schema, table = c.Schema, name
	if s, t, ok := strings.Cut(name, "."); ok {
		schema, table = s, t
	}
	if !identRe.MatchString(schema) || !identRe.MatchString(table) {
		return "", "", fmt.Errorf("%w: database.tenancy.tables entry %q must be table or schema.table", ErrInvalidTenancy, name)
	}
	return schema, table, nil
}

// ParseTenantID validates a tenant ID, which must be a UUID version 4 in the
// canonical 8-4-4-4-12 form, and returns it lowercased.
func ParseTenantID(s string) (string, error) {
	if len(s) != 36 {
		return "", ErrInvalidTenantID
	}
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return "", ErrInvalidTenantID
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return "", ErrInvalidTenantID
			}
		}
	}
	if b[14] != '4' { // version
		return "", ErrInvalidTenantID
	}
	switch b[19] { // RFC 9562 variant 10xx
	case '8', '9', 'a', 'b':
	default:
		return "", ErrInvalidTenantID
	}
	return string(b), nil
}

type tenantKey struct{}

// WithTenant returns a context carrying the validated tenant ID, for
// DB.ForContext. HTTP applications usually let the nhttp tenant middlewares
// call it.
func WithTenant(ctx context.Context, tenantID string) (context.Context, error) {
	id, err := ParseTenantID(tenantID)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, tenantKey{}, id), nil
}

// TenantFromContext returns the tenant ID stored by WithTenant.
func TenantFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(tenantKey{}).(string)
	return id, ok && id != ""
}

// Option configures New.
type Option func(*DB)

// WithTenancy enables tenant scoping on the DB built by New. An invalid
// config makes New panic (Open returns the error instead).
func WithTenancy(cfg TenancyConfig) Option {
	return func(db *DB) {
		if !cfg.Enabled {
			db.tenancy = nil
			return
		}
		if err := cfg.Validate(); err != nil {
			panic(err)
		}
		t := cfg.withDefaults()
		db.tenancy = &t
	}
}

// Tenancy returns the effective tenancy config (defaults applied); Enabled
// is false when tenancy is off.
func (db *DB) Tenancy() TenancyConfig {
	if db.tenancy == nil {
		return TenancyConfig{}
	}
	t := *db.tenancy
	t.Tables = append([]string(nil), t.Tables...)
	return t
}

// ForTenant returns a DB scoped to tenantID (a UUID v4). In rls mode each
// statement on its Executor runs in its own implicit transaction with the
// tenant setting applied, and Transaction applies it once at BEGIN; in column
// mode the Executor is the pool and only TenantEq/TenantID carry the tenant.
// The returned DB shares the pool and is safe for concurrent use; it cannot
// shut the pool down.
func (db *DB) ForTenant(tenantID string) (*DB, error) {
	if db.tenancy == nil {
		return nil, ErrTenancyDisabled
	}
	if db.transaction {
		return nil, ErrTenantInTransaction
	}
	id, err := ParseTenantID(tenantID)
	if err != nil {
		return nil, err
	}
	scoped := &DB{
		Executor: db.pool,
		Query:    newStatementBuilder(),
		pool:     db.pool,
		tenancy:  db.tenancy,
		tenantID: id,
	}
	if db.tenancy.Mode == TenancyRLS {
		scoped.Executor = &rlsexec.Executor{Pool: db.pool, Setting: db.tenancy.Setting, TenantID: id}
	}
	return scoped, nil
}

// ForContext is ForTenant with the tenant stored in ctx by WithTenant. A
// context without a tenant fails closed with ErrNoTenant.
func (db *DB) ForContext(ctx context.Context) (*DB, error) {
	id, ok := TenantFromContext(ctx)
	if !ok {
		if db.tenancy == nil {
			return nil, ErrTenancyDisabled
		}
		return nil, ErrNoTenant
	}
	return db.ForTenant(id)
}

// TenantID returns the tenant of a tenant-scoped DB, or "" otherwise.
func (db *DB) TenantID() string { return db.tenantID }

// TenantColumn returns the configured tenant column, for INSERT column lists
// (it is "" when tenancy is disabled).
func (db *DB) TenantColumn() string {
	if db.tenancy == nil {
		return ""
	}
	return db.tenancy.Column
}

// TenantEq returns the mandatory tenant predicate (column = $N). Add it first
// and unconditionally; user filters go in further Where calls, which are
// AND-ed and can only narrow it. Outside a tenant-scoped DB, ToSql fails
// with ErrNoTenant instead of building an unscoped query.
func (db *DB) TenantEq() sq.Sqlizer {
	return db.tenantPredicate("")
}

// TenantEqOn is TenantEq qualified with a table name or alias, for joins
// (TenantEqOn("u") → u.tenant_id = $N).
func (db *DB) TenantEqOn(qualifier string) sq.Sqlizer {
	return db.tenantPredicate(qualifier)
}

func (db *DB) tenantPredicate(qualifier string) sq.Sqlizer {
	p := tenantPredicate{qualifier: qualifier, tenantID: db.tenantID}
	if db.tenancy != nil {
		p.column = db.tenancy.Column
	}
	return p
}

type tenantPredicate struct {
	qualifier, column, tenantID string
}

func (p tenantPredicate) ToSql() (string, []any, error) {
	switch {
	case p.column == "":
		return "", nil, ErrTenancyDisabled
	case p.tenantID == "":
		return "", nil, ErrNoTenant
	case p.qualifier != "" && !identRe.MatchString(p.qualifier):
		return "", nil, fmt.Errorf("database: invalid tenant qualifier %q", p.qualifier)
	}
	col := p.column
	if p.qualifier != "" {
		col = p.qualifier + "." + col
	}
	return col + " = ?", []any{p.tenantID}, nil
}

// CheckTenancy verifies the database against the tenancy config. In rls mode
// the current role must not be a superuser or have BYPASSRLS (both ignore
// every policy), and each listed table must exist, have the tenant column,
// have row level security enabled with at least one policy, and have it
// forced when the role owns the table. In column mode only the tables and
// columns are checked. Open runs it; callers of New may run it themselves.
func (db *DB) CheckTenancy(ctx context.Context) error {
	t := db.tenancy
	if t == nil {
		return nil
	}
	var problems []error
	if t.Mode == TenancyRLS {
		var bypass bool
		err := db.pool.QueryRow(ctx,
			"SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user").Scan(&bypass)
		if err != nil {
			return fmt.Errorf("check tenancy role: %w", err)
		}
		if bypass {
			problems = append(problems, errors.New("the connection role is a superuser or has BYPASSRLS, which ignores row level security"))
		}
	}
	for _, name := range t.Tables {
		schema, table, _ := t.splitTable(name)
		var exists, hasColumn, rls, forced, owner, hasPolicy bool
		err := db.pool.QueryRow(ctx, `
			SELECT true,
			       EXISTS (SELECT 1 FROM pg_attribute a
			               WHERE a.attrelid = c.oid AND a.attname = $3 AND a.attnum > 0 AND NOT a.attisdropped),
			       c.relrowsecurity,
			       c.relforcerowsecurity,
			       pg_has_role(current_user, c.relowner, 'USAGE'),
			       EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid)
			FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'p')`,
			schema, table, t.Column,
		).Scan(&exists, &hasColumn, &rls, &forced, &owner, &hasPolicy)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check tenancy table %s.%s: %w", schema, table, err)
		}
		qualified := schema + "." + table
		switch {
		case !exists:
			problems = append(problems, fmt.Errorf("%s: table not found", qualified))
			continue
		case !hasColumn:
			problems = append(problems, fmt.Errorf("%s: column %s not found", qualified, t.Column))
		}
		if t.Mode != TenancyRLS {
			continue
		}
		if !rls {
			problems = append(problems, fmt.Errorf("%s: row level security is not enabled", qualified))
		} else if owner && !forced {
			problems = append(problems, fmt.Errorf("%s: the role owns the table but row level security is not forced", qualified))
		}
		if !hasPolicy {
			problems = append(problems, fmt.Errorf("%s: no policy defined", qualified))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %w", ErrTenancyCheck, errors.Join(problems...))
	}
	return nil
}
