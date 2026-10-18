package database

import "errors"

var (
	// ErrNestedTransaction is returned when Transaction is called on a
	// transaction-scoped DB. Savepoints are not emulated.
	ErrNestedTransaction = errors.New("database: nested transactions are not supported")

	// ErrShutdownInTransaction is returned when Shutdown is called on a
	// transaction-scoped DB. Only the root DB owns the pool.
	ErrShutdownInTransaction = errors.New("database: shutdown is not allowed on a transaction-scoped database")

	// ErrShutdownTenantScoped is returned when Shutdown is called on a
	// tenant-scoped DB, which shares the root DB's pool.
	ErrShutdownTenantScoped = errors.New("database: shutdown is not allowed on a tenant-scoped database")

	// ErrTenancyDisabled is returned by the tenant API when database.tenancy
	// is not enabled.
	ErrTenancyDisabled = errors.New("database: tenancy is not enabled")
	// ErrNoTenant is returned when an operation needs a tenant and none was
	// given (empty context, or TenantEq outside a tenant-scoped DB).
	ErrNoTenant = errors.New("database: no tenant in scope")
	// ErrInvalidTenantID is returned for a tenant ID that is not a canonical
	// UUID version 4.
	ErrInvalidTenantID = errors.New("database: tenant id must be a canonical uuid v4")
	// ErrTenantInTransaction is returned by ForTenant on a transaction-scoped
	// DB: scope first, then begin the transaction on the tenant DB.
	ErrTenantInTransaction = errors.New("database: cannot scope a running transaction to a tenant")
	// ErrInvalidTenancy wraps database.tenancy configuration errors.
	ErrInvalidTenancy = errors.New("database: invalid tenancy configuration")
	// ErrTenancyCheck wraps the problems found by CheckTenancy at startup.
	ErrTenancyCheck = errors.New("database: tenancy check failed")

	ErrNilPool           = errors.New("database: nil pgxpool.Pool")
	ErrUnsupportedDriver = errors.New("database: unsupported driver")
	ErrMissingConfig     = errors.New("database: missing required configuration")
)
