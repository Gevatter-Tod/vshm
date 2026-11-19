package mysql

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/scheduler"
	mysqlDriver "github.com/go-sql-driver/mysql"
)

// --- fake driver -----------------------------------------------------------

type fakeConnector struct {
	cfg        *mysqlDriver.Config
	conn       *fakeConn
	connectErr error
}

func (f *fakeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if f.connectErr != nil {
		return nil, f.connectErr
	}
	return f.conn, nil
}

func (f *fakeConnector) Driver() driver.Driver { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type fakeConn struct {
	gotQuery          string
	queryErr          error
	rows              *fakeRows
	blockUntilCtxDone bool
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}
func (c *fakeConn) Close() error              { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("tx not supported") }

func (c *fakeConn) Ping(ctx context.Context) error { return nil }

func (c *fakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.gotQuery = query
	if c.blockUntilCtxDone {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return c.rows, nil
}

type fakeRows struct {
	cols    []string
	n       int
	i       int
	nextErr error
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= r.n {
		if r.nextErr != nil {
			return r.nextErr
		}
		return io.EOF
	}
	for j := range dest {
		dest[j] = "secret-row-value"
	}
	r.i++
	return nil
}

// --- helpers ---------------------------------------------------------------

const (
	fixtureCreds = "../../../tests/fixtures/mysql/client.ini"
	fixtureCA    = "../../../tests/fixtures/mysql/ca.pem"
)

// credFixture copies the credential fixture into a temp file with mode 0600:
// git cannot preserve the 0640 mode of the committed fixture, and the adapter
// fails closed on world-readable credential files.
func credFixture(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fixtureCreds)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "client.ini")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testLogger(buf *bytes.Buffer) *logging.Logger {
	return &logging.Logger{Logger: log.New(buf, "", 0)}
}

func discardLogger() *logging.Logger { return testLogger(&bytes.Buffer{}) }

// factoryFor returns a connectorFactory capturing the mysql.Config and
// serving the given fake conn.
func factoryFor(conn *fakeConn, connectErr error, captured **mysqlDriver.Config) connectorFactory {
	return func(mc *mysqlDriver.Config) (driver.Connector, error) {
		*captured = mc
		return &fakeConnector{cfg: mc, conn: conn, connectErr: connectErr}, nil
	}
}

func tcpConfig(t *testing.T) *Config {
	t.Helper()
	return NewConfig("db-check", "db.example.org", 0, "", "app", credFixture(t), fixtureCA, "SELECT 1")
}

// --- tests -----------------------------------------------------------------

func TestType(t *testing.T) {
	if New(discardLogger()).Type() != "mysql" {
		t.Fatal("Type() must be mysql")
	}
}

func TestRunSuccessTCPWithTLS(t *testing.T) {
	var captured *mysqlDriver.Config
	conn := &fakeConn{rows: &fakeRows{cols: []string{"one"}, n: 3}}
	c := NewWithFactory(discardLogger(), factoryFor(conn, nil, &captured))

	res := c.Run(context.Background(), tcpConfig(t))
	if !res.Success {
		t.Fatalf("expected success, got %+v", res)
	}
	if res.ReasonCode != reasonSuccess {
		t.Errorf("ReasonCode = %q, want %q", res.ReasonCode, reasonSuccess)
	}
	if res.CheckID != "db-check" || res.CheckType != "mysql" {
		t.Errorf("bad identity: %+v", res)
	}
	if res.StartedAt.IsZero() || res.CompletedAt.IsZero() || res.Duration < 0 {
		t.Errorf("bad timestamps: %+v", res)
	}
	if res.StartedAt.Location() != time.UTC {
		t.Errorf("timestamps must be UTC")
	}

	// Driver config assertions: TCP + verified TLS + credentials + db precedence.
	if captured == nil {
		t.Fatal("factory was not called")
	}
	if captured.Net != "tcp" || captured.Addr != "db.example.org:3306" {
		t.Errorf("bad addr: net=%q addr=%q (port default 3306 expected)", captured.Net, captured.Addr)
	}
	if captured.TLS == nil {
		t.Fatal("TLS config must be set for TCP")
	}
	if captured.TLS.ServerName != "db.example.org" {
		t.Errorf("TLS ServerName = %q, want db.example.org (hostname verification)", captured.TLS.ServerName)
	}
	if captured.TLS.RootCAs == nil {
		t.Error("TLS RootCAs must be set from tls_ca")
	}
	if captured.TLS.InsecureSkipVerify {
		t.Error("InsecureSkipVerify must be false")
	}
	if captured.TLS.MinVersion < 0x0303 {
		t.Errorf("TLS MinVersion too low: %x", captured.TLS.MinVersion)
	}
	if captured.User != "vshm_monitor" || captured.Passwd != "example-only-not-a-real-secret" {
		t.Errorf("credentials not loaded from [client] file")
	}
	if captured.DBName != "app" {
		t.Errorf("explicit check database must take precedence: DBName = %q, want app", captured.DBName)
	}
	// The validated statement is sent without the trailing semicolon.
	if conn.gotQuery != "SELECT 1" {
		t.Errorf("sent query = %q, want %q", conn.gotQuery, "SELECT 1")
	}
}

func TestRunSuccessUnixSocketNoTLS(t *testing.T) {
	var captured *mysqlDriver.Config
	conn := &fakeConn{rows: &fakeRows{cols: []string{"one"}, n: 1}}
	c := NewWithFactory(discardLogger(), factoryFor(conn, nil, &captured))
	cfg := NewConfig("db-local", "", 0, "/run/mysqld/mysqld.sock", "", credFixture(t), "", "SELECT 1;")

	res := c.Run(context.Background(), cfg)
	if !res.Success {
		t.Fatalf("expected success over socket, got %+v", res)
	}
	if captured.Net != "unix" || captured.Addr != "/run/mysqld/mysqld.sock" {
		t.Errorf("bad socket addr: net=%q addr=%q", captured.Net, captured.Addr)
	}
	if captured.TLS != nil {
		t.Error("Unix socket must not use network TLS")
	}
	// database falls back to the credential-file database
	if captured.DBName != "appdb" {
		t.Errorf("DBName = %q, want appdb from credential file", captured.DBName)
	}
	if conn.gotQuery != "SELECT 1" {
		t.Errorf("trailing semicolon must be stripped before sending: %q", conn.gotQuery)
	}
}

func TestRunDatabasePrecedence(t *testing.T) {
	var captured *mysqlDriver.Config
	conn := &fakeConn{rows: &fakeRows{cols: []string{"x"}, n: 1}}
	c := NewWithFactory(discardLogger(), factoryFor(conn, nil, &captured))

	// explicit database wins
	res := c.Run(context.Background(), NewConfig("c1", "h", 3307, "", "explicitdb", credFixture(t), fixtureCA, "SELECT 1"))
	if !res.Success || captured.DBName != "explicitdb" {
		t.Errorf("explicit database must win: res=%+v db=%q", res, captured.DBName)
	}
	if captured.Addr != "h:3307" {
		t.Errorf("explicit port must be used: %q", captured.Addr)
	}

	// file database used when check database empty (fresh conn: rows consumed)
	conn2 := &fakeConn{rows: &fakeRows{cols: []string{"x"}, n: 1}}
	var captured2 *mysqlDriver.Config
	c2 := NewWithFactory(discardLogger(), factoryFor(conn2, nil, &captured2))
	res = c2.Run(context.Background(), NewConfig("c2", "h", 0, "", "", credFixture(t), fixtureCA, "SELECT 1"))
	if !res.Success || captured2.DBName != "appdb" {
		t.Errorf("file database expected: res=%+v db=%q", res, captured2.DBName)
	}
}

func TestRunZeroRowsFails(t *testing.T) {
	var captured *mysqlDriver.Config
	conn := &fakeConn{rows: &fakeRows{cols: []string{"one"}, n: 0}}
	c := NewWithFactory(discardLogger(), factoryFor(conn, nil, &captured))

	res := c.Run(context.Background(), tcpConfig(t))
	if res.Success {
		t.Fatal("zero rows must fail")
	}
	if res.ReasonCode != reasonNoRows {
		t.Errorf("ReasonCode = %q, want %q", res.ReasonCode, reasonNoRows)
	}
}

func TestRunQueryErrorHidesSQLText(t *testing.T) {
	var captured *mysqlDriver.Config
	sneaky := "You have an error in your SQL syntax near 'SELECT secret_column FROM secret_table'"
	conn := &fakeConn{queryErr: &mysqlDriver.MySQLError{Number: 1064, Message: sneaky}}
	c := NewWithFactory(discardLogger(), factoryFor(conn, nil, &captured))

	res := c.Run(context.Background(), NewConfig("db", "db.example.org", 0, "", "", credFixture(t), fixtureCA, "SELECT secret_column FROM secret_table"))
	if res.Success {
		t.Fatal("query error must fail")
	}
	if res.ReasonCode != reasonQuery {
		t.Errorf("ReasonCode = %q, want %q", res.ReasonCode, reasonQuery)
	}
	if !strings.Contains(res.Reason, "1064") {
		t.Errorf("reason should carry the numeric MySQL error code: %q", res.Reason)
	}
	if strings.Contains(res.Reason, "secret_column") || strings.Contains(res.Reason, "syntax") {
		t.Errorf("reason leaks SQL text: %q", res.Reason)
	}
}

func TestRunRowsErrorAfterPartial(t *testing.T) {
	var captured *mysqlDriver.Config
	conn := &fakeConn{rows: &fakeRows{cols: []string{"x"}, n: 5, nextErr: &mysqlDriver.MySQLError{Number: 1146, Message: "Table 'db.secret_tbl' doesn't exist"}}}
	c := NewWithFactory(discardLogger(), factoryFor(conn, nil, &captured))

	res := c.Run(context.Background(), tcpConfig(t))
	if res.Success {
		t.Fatal("row-stream error must fail")
	}
	if res.ReasonCode != reasonQuery || !strings.Contains(res.Reason, "1146") || strings.Contains(res.Reason, "secret_tbl") {
		t.Errorf("bad reason: code=%q reason=%q", res.ReasonCode, res.Reason)
	}
}

func TestRunConnectError(t *testing.T) {
	var captured *mysqlDriver.Config
	c := NewWithFactory(discardLogger(), factoryFor(nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, &captured))

	res := c.Run(context.Background(), tcpConfig(t))
	if res.Success || res.ReasonCode != reasonConnect {
		t.Fatalf("connect error: got %+v", res)
	}
	if strings.Contains(res.Reason, "connection refused") {
		t.Errorf("raw network error text must not surface: %q", res.Reason)
	}
}

func TestRunTimeout(t *testing.T) {
	var captured *mysqlDriver.Config
	conn := &fakeConn{blockUntilCtxDone: true}
	c := NewWithFactory(discardLogger(), factoryFor(conn, nil, &captured))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	res := c.Run(ctx, tcpConfig(t))
	if res.Success || res.ReasonCode != reasonTimeout {
		t.Fatalf("deadline: got %+v", res)
	}
	if res.Duration > 5*time.Second {
		t.Errorf("duration should respect deadline: %v", res.Duration)
	}
}

func TestRunInvalidQueryRejectedBeforeConnect(t *testing.T) {
	called := false
	factory := func(mc *mysqlDriver.Config) (driver.Connector, error) {
		called = true
		return &fakeConnector{conn: &fakeConn{rows: &fakeRows{cols: []string{"x"}, n: 1}}}, nil
	}
	c := NewWithFactory(discardLogger(), factory)
	for _, q := range []string{"", "DROP TABLE t", "SELECT 1; SELECT 2", "SELECT * FROM t FOR UPDATE", "WITH x AS (SELECT 1) SELECT 1"} {
		res := c.Run(context.Background(), NewConfig("db", "h", 0, "", "", credFixture(t), fixtureCA, q))
		if res.Success || res.ReasonCode != reasonInvalidQuery {
			t.Errorf("query %q: got %+v, want invalid_query", q, res)
		}
	}
	if called {
		t.Error("driver must not be touched for an invalid query")
	}
}

func TestRunMissingCredentialFile(t *testing.T) {
	var captured *mysqlDriver.Config
	c := NewWithFactory(discardLogger(), factoryFor(&fakeConn{rows: &fakeRows{cols: []string{"x"}, n: 1}}, nil, &captured))
	res := c.Run(context.Background(), NewConfig("db", "h", 0, "", "", "/nonexistent/creds.ini", fixtureCA, "SELECT 1"))
	if res.Success || res.ReasonCode != reasonCredentialError {
		t.Fatalf("got %+v", res)
	}
	if strings.Contains(res.Reason, "/nonexistent") {
		t.Errorf("secret-bearing path must not appear in reason: %q", res.Reason)
	}
}

func TestRunBadCA(t *testing.T) {
	var captured *mysqlDriver.Config
	c := NewWithFactory(discardLogger(), factoryFor(&fakeConn{rows: &fakeRows{cols: []string{"x"}, n: 1}}, nil, &captured))

	res := c.Run(context.Background(), NewConfig("db", "h", 0, "", "", credFixture(t), "/nonexistent/ca.pem", "SELECT 1"))
	if res.Success || res.ReasonCode != reasonTLSCAError {
		t.Fatalf("missing CA: got %+v", res)
	}

	// CA file exists but is not PEM
	bad := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(bad, []byte("not a pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = c.Run(context.Background(), NewConfig("db", "h", 0, "", "", credFixture(t), bad, "SELECT 1"))
	if res.Success || res.ReasonCode != reasonTLSCAError {
		t.Fatalf("garbage CA: got %+v", res)
	}
}

func TestRunTCPWithoutTLSCAFailsConfig(t *testing.T) {
	var captured *mysqlDriver.Config
	c := NewWithFactory(discardLogger(), factoryFor(&fakeConn{rows: &fakeRows{cols: []string{"x"}, n: 1}}, nil, &captured))
	res := c.Run(context.Background(), NewConfig("db", "h", 0, "", "", credFixture(t), "", "SELECT 1"))
	if res.Success || res.ReasonCode != reasonInvalidConfig {
		t.Fatalf("TCP without tls_ca must fail: %+v", res)
	}
}

func TestRunNoSecretsInLogsOrResult(t *testing.T) {
	var logBuf bytes.Buffer
	c := NewWithFactory(testLogger(&logBuf), factoryFor(nil, &net.OpError{Op: "dial", Err: errors.New("refused")}, new(*mysqlDriver.Config)))
	cfg := NewConfig("db", "h", 0, "", "", credFixture(t), fixtureCA, "SELECT secret_col FROM secret_tbl")
	res := c.Run(context.Background(), cfg)

	leaks := []string{"example-only-not-a-real-secret", "vshm_monitor", "secret_col", "secret_tbl", fixtureCreds}
	for _, l := range leaks {
		if strings.Contains(logBuf.String(), l) {
			t.Errorf("log leaks %q: %s", l, logBuf.String())
		}
		if strings.Contains(res.Reason, l) || strings.Contains(res.ReasonCode, l) {
			t.Errorf("result leaks %q: %+v", l, res)
		}
	}
}

func TestRunViaSchedulerSpec(t *testing.T) {
	var captured *mysqlDriver.Config
	conn := &fakeConn{rows: &fakeRows{cols: []string{"one"}, n: 1}}
	c := NewWithFactory(discardLogger(), factoryFor(conn, nil, &captured))

	spec := &scheduler.Spec{
		ID:  "db-readiness",
		Typ: "mysql",
		MySQL: &scheduler.MySQLSpec{
			Host:           "db.example.org",
			Port:           3307,
			Database:       "app",
			CredentialFile: credFixture(t),
			TLSCA:          fixtureCA,
			Query:          "SELECT 1;",
		},
	}
	res := c.Run(context.Background(), spec)
	if !res.Success || res.CheckID != "db-readiness" || res.CheckType != "mysql" {
		t.Fatalf("spec path failed: %+v", res)
	}
	if captured.Addr != "db.example.org:3307" {
		t.Errorf("addr = %q", captured.Addr)
	}
}

func TestRunSpecWithoutMySQLFails(t *testing.T) {
	c := NewWithFactory(discardLogger(), factoryFor(&fakeConn{}, nil, new(*mysqlDriver.Config)))
	res := c.Run(context.Background(), &scheduler.Spec{ID: "x", Typ: "mysql"})
	if res.Success || res.ReasonCode != reasonInvalidConfig {
		t.Fatalf("got %+v", res)
	}
}

type otherConfig struct{}

func (otherConfig) CheckID() string   { return "x" }
func (otherConfig) CheckType() string { return "other" }

func TestRunWrongConfigType(t *testing.T) {
	c := New(discardLogger())
	res := c.Run(context.Background(), otherConfig{})
	if res.Success || res.ReasonCode != reasonInvalidConfig {
		t.Fatalf("got %+v", res)
	}
}

func TestResultImplementsModel(t *testing.T) {
	var _ model.Check = New(discardLogger())
	var _ model.CheckConfig = NewConfig("a", "h", 0, "", "", "", "", "SELECT 1")
}

// --- TLS config ------------------------------------------------------------

func TestBuildTLSConfig(t *testing.T) {
	cfg, err := buildTLSConfig(fixtureCA, "db.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerName != "db.example.org" {
		t.Errorf("ServerName = %q", cfg.ServerName)
	}
	if cfg.RootCAs == nil {
		t.Error("RootCAs must be set")
	}
	if cfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify must be false")
	}
	if cfg.MinVersion < 0x0303 {
		t.Errorf("MinVersion = %x, want >= TLS1.2", cfg.MinVersion)
	}
	// The fixture CA must actually be in the pool.
	if len(cfg.RootCAs.Subjects()) == 0 {
		t.Error("CA pool is empty")
	}
}

func TestBuildTLSConfigErrors(t *testing.T) {
	if _, err := buildTLSConfig("/nonexistent/ca.pem", "h"); err == nil {
		t.Error("missing CA file must error")
	}
	bad := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(bad, []byte("garbage"), 0o600)
	if _, err := buildTLSConfig(bad, "h"); err == nil {
		t.Error("non-PEM CA must error")
	}
}

// --- credential INI --------------------------------------------------------

func TestLoadClientCredentialsFixture(t *testing.T) {
	creds, err := loadClientCredentials(credFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if creds.user != "vshm_monitor" || creds.password != "example-only-not-a-real-secret" || creds.database != "appdb" {
		t.Errorf("bad creds: %+v", creds)
	}
}

func writeCreds(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mysql.ini")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile applies umask; chmod to the exact mode under test.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadClientCredentialsParsing(t *testing.T) {
	path := writeCreds(t, `
; comment
# another comment
[client]
user = "quoted user"
password='single quoted'
database = somedb
[other]
ignored = yes
`, 0o640)
	creds, err := loadClientCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if creds.user != "quoted user" || creds.password != "single quoted" || creds.database != "somedb" {
		t.Errorf("bad parse: %+v", creds)
	}
}

func TestLoadClientCredentialsErrors(t *testing.T) {
	cases := []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{"missing [client]", "[other]\nuser = u\n", 0o640},
		{"missing user", "[client]\npassword = p\n", 0o640},
		{"missing password", "[client]\nuser = u\n", 0o640},
		{"world readable", "[client]\nuser = u\npassword = p\n", 0o644},
		{"group writable", "[client]\nuser = u\npassword = p\n", 0o660},
		{"malformed line", "[client]\nuser u\npassword = p\n", 0o640},
		{"duplicate key", "[client]\nuser = u\nuser = v\npassword = p\n", 0o640},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeCreds(t, tc.content, tc.mode)
			_, err := loadClientCredentials(path)
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "password = p") || strings.Contains(err.Error(), "= p") {
				t.Errorf("error must not contain file content: %v", err)
			}
		})
	}
	if _, err := loadClientCredentials("/nonexistent.ini"); err == nil {
		t.Error("missing file must error")
	}
}
