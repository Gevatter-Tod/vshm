// Package mysql provides the read-only MySQL query health adapter (WP-07).
//
// Security properties (vshm spec §4.4 / docs/wp07-check-mysql.md):
//   - TCP connections use verified TLS with the configured CA and hostname
//     verification (TLS >= 1.2); a local Unix socket is permitted without
//     network TLS.
//   - Exactly one read-only SELECT statement (at most one optional trailing
//     semicolon) is accepted; see internal/sqlsafe for the conservative
//     grammar. The dedicated read-only DB account is the real boundary.
//   - Success requires execution and fetching at least one row; row values
//     are never scanned and are discarded immediately.
//   - Credentials come from an INI [client] file (user/password/optional
//     database); an explicit check database takes precedence. Secrets, SQL
//     text, row values and secret-bearing paths never enter logs, reasons,
//     or results.
//   - Connect, query and row retrieval all run under the single absolute
//     deadline carried by ctx.
package mysql

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/scheduler"
	"github.com/Gevatter-Tod/vshm/internal/sqlsafe"
	mysqlDriver "github.com/go-sql-driver/mysql"
)

// Reason codes returned in CheckResult.ReasonCode. They are stable,
// short and never contain secrets, SQL text or row values.
const (
	reasonSuccess         = "success"
	reasonInvalidConfig   = "invalid_config"
	reasonInvalidQuery    = "invalid_query"
	reasonCredentialError = "credential_error"
	reasonTLSCAError      = "tls_ca_error"
	reasonTLSVerify       = "tls_verify_failed"
	reasonConnect         = "connect_failed"
	reasonTimeout         = "timeout"
	reasonQuery           = "query_failed"
	reasonNoRows          = "no_rows"
)

// connectorFactory builds a database/sql connector from a driver config.
// The real factory uses the pinned go-sql-driver; tests inject a fake.
type connectorFactory func(*mysqlDriver.Config) (driver.Connector, error)

func realConnectorFactory() connectorFactory {
	return func(mc *mysqlDriver.Config) (driver.Connector, error) {
		return mysqlDriver.NewConnector(mc)
	}
}

// Check is the MySQL check adapter.
type Check struct {
	logger  *logging.Logger
	factory connectorFactory
}

// New creates a new MySQL check with the real driver.
func New(logger *logging.Logger) *Check {
	return &Check{logger: logger, factory: realConnectorFactory()}
}

// NewWithFactory creates a MySQL check with a custom connector factory
// (for tests with a fake driver/database; no live credentials needed).
func NewWithFactory(logger *logging.Logger, factory connectorFactory) *Check {
	return &Check{logger: logger, factory: factory}
}

// Type returns the check type.
func (c *Check) Type() string { return "mysql" }

// Run executes the MySQL check exactly once and returns one normalized
// observation with UTC timestamps and a sanitized reason.
func (c *Check) Run(ctx context.Context, cfg model.CheckConfig) model.CheckResult {
	startedAt := time.Now().UTC()

	var mc *Config
	switch cc := cfg.(type) {
	case *Config:
		mc = cc
	case *scheduler.Spec:
		var err error
		mc, err = fromSpec(cc)
		if err != nil {
			return c.result(cfg, startedAt, false, reasonInvalidConfig, "mysql check config missing")
		}
	default:
		return c.result(cfg, startedAt, false, reasonInvalidConfig, "unsupported check config type")
	}
	if mc == nil {
		return c.result(cfg, startedAt, false, reasonInvalidConfig, "check config is nil")
	}

	return c.execute(ctx, mc, cfg, startedAt)
}

// execute connects, runs the validated single SELECT under the ctx deadline,
// requires at least one row, and discards all values immediately.
func (c *Check) execute(ctx context.Context, mc *Config, cfg model.CheckConfig, startedAt time.Time) model.CheckResult {
	fail := func(code, msg string) model.CheckResult {
		c.logger.Printf("mysql check %q failed: %s", cfg.CheckID(), code)
		return c.result(cfg, startedAt, false, code, msg)
	}

	// 1. Validate the query: exactly one read-only SELECT.
	stmt, err := sqlsafe.Validate(mc.query)
	if err != nil {
		return fail(reasonInvalidQuery, "query is not a single read-only SELECT statement")
	}

	// 2. Load credentials from the INI [client] file (never logged).
	if mc.credentialFile == "" {
		return fail(reasonInvalidConfig, "credential_file is required")
	}
	creds, err := loadClientCredentials(mc.credentialFile)
	if err != nil {
		return fail(reasonCredentialError, "failed to load credentials")
	}
	// An explicit check database takes precedence over the file database.
	dbName := mc.database
	if dbName == "" {
		dbName = creds.database
	}

	// 3. Build the driver config. The password lives only in this struct and
	// is never formatted into a string that could be logged.
	dc := &mysqlDriver.Config{
		User:                 creds.user,
		Passwd:               creds.password,
		DBName:               dbName,
		AllowNativePasswords: true,
		// multiStatements stays disabled (driver default): the server
		// rejects more than one statement per query.
	}
	if mc.socket != "" {
		// Local Unix socket: permitted without network TLS.
		dc.Net = "unix"
		dc.Addr = mc.socket
	} else {
		host := mc.host
		port := mc.port
		if port == 0 {
			port = DefaultPort
		}
		if mc.tlsCA == "" {
			return fail(reasonInvalidConfig, "tls_ca is required for TCP connections")
		}
		tlsCfg, err := buildTLSConfig(mc.tlsCA, host)
		if err != nil {
			return fail(reasonTLSCAError, "failed to load TLS CA")
		}
		dc.Net = "tcp"
		dc.Addr = net.JoinHostPort(host, strconv.Itoa(port))
		dc.TLS = tlsCfg // verified TLS: RootCAs + ServerName=host, >= TLS 1.2
	}

	connector, err := c.factory(dc)
	if err != nil {
		return fail(reasonConnect, "failed to initialize driver connector")
	}
	db := sql.OpenDB(connector)
	defer db.Close()

	// 4. Execute under the one absolute deadline (DNS, connect, query and row
	// retrieval all observe ctx).
	rows, err := db.QueryContext(ctx, stmt)
	if err != nil {
		code, msg := classifyErr(ctx, err)
		return fail(code, msg)
	}
	defer rows.Close()

	// 5. Fetch rows without ever scanning values; success requires at least
	// one row. Values are discarded immediately.
	gotRow := false
	for rows.Next() {
		gotRow = true
	}
	if err := rows.Err(); err != nil {
		code, msg := classifyErr(ctx, err)
		return fail(code, msg)
	}
	if !gotRow {
		return fail(reasonNoRows, "query returned no rows")
	}

	return c.result(cfg, startedAt, true, reasonSuccess, "query ok")
}

// result builds the normalized observation.
func (c *Check) result(cfg model.CheckConfig, startedAt time.Time, success bool, code, reason string) model.CheckResult {
	completedAt := time.Now().UTC()
	return model.CheckResult{
		CheckID:     cfg.CheckID(),
		CheckType:   cfg.CheckType(),
		Success:     success,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Duration:    completedAt.Sub(startedAt),
		ReasonCode:  code,
		Reason:      sanitizeReason(reason),
	}
}

// classifyErr maps an error from connect/query/rows to a stable reason code
// and a static, sanitized message. Raw error text is deliberately discarded:
// MySQL errors can embed SQL text, and network errors can embed addresses.
func classifyErr(ctx context.Context, err error) (string, string) {
	if ctx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
		return reasonTimeout, "deadline exceeded during connect or query"
	}
	if errors.Is(err, context.Canceled) {
		return reasonTimeout, "check cancelled"
	}
	var merr *mysqlDriver.MySQLError
	if errors.As(err, &merr) {
		// Only the numeric error code is reported; the driver message may
		// quote SQL text and must never be surfaced.
		return reasonQuery, fmt.Sprintf("query failed (MySQL error %d)", merr.Number)
	}
	var ua x509.UnknownAuthorityError
	if errors.As(err, &ua) {
		return reasonTLSVerify, "certificate signed by unknown authority"
	}
	var hn x509.HostnameError
	if errors.As(err, &hn) {
		return reasonTLSVerify, "certificate hostname mismatch"
	}
	var ci x509.CertificateInvalidError
	if errors.As(err, &ci) {
		return reasonTLSVerify, "certificate invalid"
	}
	var re tls.RecordHeaderError
	if errors.As(err, &re) {
		return reasonTLSVerify, "TLS handshake failed"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return reasonConnect, "connect failed"
	}
	return reasonQuery, "query failed"
}

// sanitizeReason bounds length and strips control characters so the reason
// stays short and single-line.
func sanitizeReason(s string) string {
	const maxLen = 256
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.Join(strings.Fields(s), " ")
}
