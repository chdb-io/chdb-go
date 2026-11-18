package chdbdriver

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/chdb-io/chdb-go/v2/chdb"
	chdbpurego "github.com/chdb-io/chdb-go/v2/chdb-purego"
	"github.com/huandu/go-sqlbuilder"
	"github.com/parquet-go/parquet-go"
)

type DriverType int

const (
	ARROW DriverType = iota
	PARQUET
	PARQUET_STREAMING
	INVALID
)

const (
	sessionOptionKey         = "session"
	udfPathOptionKey         = "udfPath"
	driverTypeKey            = "driverType"
	useUnsafeStringReaderKey = "useUnsafeStringReader"
	driverBufferSizeKey      = "bufferSize"
	defaultBufferSize        = 512
)

func (d DriverType) String() string {
	switch d {
	case ARROW:
		return "Arrow"
	case PARQUET:
		return "Parquet"
	case INVALID:
		return "Invalid"
	}
	return ""
}

func (d DriverType) PrepareRows(result chdbpurego.ChdbResult, buf []byte, bufSize int, useUnsafe bool) (driver.Rows, error) {
	switch d {
	case PARQUET:
		reader := parquet.NewGenericReader[any](bytes.NewReader(buf))
		return &parquetRows{
			localResult: result, reader: reader,
			bufferSize: bufSize, needNewBuffer: true,
			useUnsafeStringReader: useUnsafe,
			schemaFields:          reader.Schema().Fields(),
		}, nil

	}
	return nil, fmt.Errorf("unsupported driver type")
}

func (d DriverType) PrepareStreamingRows(result chdbpurego.ChdbStreamResult, bufSize int, useUnsafe bool) (driver.Rows, error) {
	switch d {
	case PARQUET_STREAMING:
		nextRes := result.GetNext()
		if nextRes == nil {
			return nil, fmt.Errorf("result is nil")
		}
		// A failed query arrives as a first chunk carrying the error and no
		// bytes; parquet.NewGenericReader panics on an empty buffer.
		if err := nextRes.Error(); err != nil {
			return nil, err
		}
		if nextRes.Len() == 0 {
			return nil, fmt.Errorf("result is nil")
		}

		reader := parquet.NewGenericReader[any](bytes.NewReader(nextRes.Buf()))
		return &parquetStreamingRows{
			stream: result, curChunk: nextRes, reader: reader,
			bufferSize: bufSize, needNewBuffer: true,
			useUnsafeStringReader: useUnsafe,
			schemaFields:          reader.Schema().Fields(),
		}, nil

	}
	return nil, fmt.Errorf("unsupported driver type")
}

func (d DriverType) SupportStreaming() bool {
	switch d {
	case PARQUET_STREAMING:
		return true
	}
	return false
}

func (d DriverType) GetFormat() string {
	switch d {
	case PARQUET:
		return "Parquet"
	case PARQUET_STREAMING:
		return "Parquet"
	}
	return ""

}

func parseDriverType(s string) DriverType {
	switch strings.ToUpper(s) {
	// case "ARROW":
	// 	return ARROW
	case "PARQUET":
		return PARQUET
	case "PARQUET_STREAMING":
		return PARQUET_STREAMING
	}
	return INVALID
}

func init() {
	sql.Register("chdb", Driver{})
}

// Row is the result of calling [DB.QueryRow] to select a single row.
type singleRow struct {
	// One of these two will be non-nil:
	err  error // deferred error for easy chaining
	rows driver.Rows
}

// Scan copies the columns from the matched row into the values
// pointed at by dest. See the documentation on [Rows.Scan] for details.
// If more than one row matches the query,
// Scan uses the first row and discards the rest. If no row matches
// the query, Scan returns [ErrNoRows].
func (r *singleRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	vals := make([]driver.Value, 0)
	for _, v := range dest {
		vals = append(vals, v)
	}
	err := r.rows.Next(vals)
	if err != nil {
		return err
	}
	// Make sure the query can be processed to completion with no errors.
	return r.rows.Close()
}

// Err provides a way for wrapping packages to check for
// query errors without calling [Row.Scan].
// Err returns the error, if any, that was encountered while running the query.
// If this error is not nil, this error will also be returned from [Row.Scan].
func (r *singleRow) Err() error {
	return r.err
}

type execResult struct {
	localRes chdbpurego.ChdbResult
	err      error
}

func (e *execResult) LastInsertId() (int64, error) {
	if e.err != nil {
		return 0, e.err
	}
	return -1, fmt.Errorf("does not support LastInsertId")

}
func (e *execResult) RowsAffected() (int64, error) {
	if e.err != nil {
		return 0, e.err
	}
	// chdb return the number of rows inserted/updated/deleted trough rows_read
	return int64(e.localRes.RowsRead()), nil
}

type queryHandle func(string, ...string) (chdbpurego.ChdbResult, error)

type queryStream func(string, ...string) (chdbpurego.ChdbStreamResult, error)

type connector struct {
	udfPath     string
	driverType  DriverType
	bufferSize  int
	isStreaming bool
	useUnsafe   bool
	connStr     string
	keeper      *chdb.Session
}

// Connect returns a connection to a database.
func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if c.driverType == INVALID {
		return nil, fmt.Errorf("DriverType not supported")
	}
	// Each database/sql connection gets its own native chDB connection to the
	// shared data path, so a pool of connections (MaxOpenConns > 1) yields real
	// parallel query execution instead of serializing on a single connection.
	session, err := chdb.NewSession(c.connStr)
	if err != nil {
		return nil, err
	}
	cc := &conn{
		udfPath: c.udfPath, session: session,
		driverType: c.driverType, bufferSize: c.bufferSize,
		useUnsafe: c.useUnsafe, isStreaming: c.isStreaming,
	}
	cc.SetupQueryFun()
	return cc, nil
}

// Close releases the connector's keeper session. database/sql calls this from
// DB.Close() because the connector implements io.Closer. Dropping the keeper
// reference lets a registry-owned temp directory be removed once all pooled
// connections are closed as well.
func (c *connector) Close() error {
	if c.keeper != nil {
		c.keeper.Close()
		c.keeper = nil
	}
	return nil
}

// Driver returns the underying Driver of the connector,
// compatibility with the Driver method on sql.DB
func (c *connector) Driver() driver.Driver { return Driver{} }

func parseConnectStr(str string) (ret map[string]string, err error) {
	ret = make(map[string]string)
	if len(str) == 0 {
		return
	}
	for _, kv := range strings.Split(str, ";") {
		parsed := strings.SplitN(kv, "=", 2)
		if len(parsed) != 2 {
			return nil, fmt.Errorf("invalid format for connection string, str: %s", kv)
		}

		ret[strings.TrimSpace(parsed[0])] = strings.TrimSpace(parsed[1])
	}

	return
}
func NewConnect(opts map[string]string) (ret *connector, err error) {
	ret = &connector{}
	driverType, ok := opts[driverTypeKey]
	if ok {
		ret.driverType = parseDriverType(driverType)
	} else {
		ret.driverType = PARQUET //default to parquet
	}
	bufferSize, ok := opts[driverBufferSizeKey]
	if ok {
		sz, err := strconv.Atoi(bufferSize)
		if err != nil {
			ret.bufferSize = defaultBufferSize
		} else {
			ret.bufferSize = sz
		}
	} else {
		ret.bufferSize = defaultBufferSize
	}
	useUnsafe, ok := opts[useUnsafeStringReaderKey]
	if ok {
		if strings.ToLower(useUnsafe) == "true" {
			ret.useUnsafe = true
		}
	}

	udfPath, ok := opts[udfPathOptionKey]
	if ok {
		ret.udfPath = udfPath
	}

	// Open a "keeper" session that pins the data path (and any temp directory)
	// for the lifetime of this connector. Each pooled connection then opens its
	// own session on the same path; the keeper guarantees the engine and temp
	// dir survive pool churn (when the live connection count briefly hits zero).
	sessionPath := opts[sessionOptionKey] // "" when not provided
	ret.keeper, err = chdb.NewSession(sessionPath)
	if err != nil {
		return nil, err
	}
	ret.connStr = ret.keeper.ConnStr()

	ret.isStreaming = ret.driverType.SupportStreaming()
	return
}

type Driver struct{}

// Open returns a new connection to the database.
func (d Driver) Open(name string) (driver.Conn, error) {
	cc, err := d.OpenConnector(name)
	if err != nil {
		return nil, err
	}
	c, err := cc.Connect(context.Background())
	if err != nil {
		// Connect failed; release the keeper session NewConnect just opened so
		// it does not leak (database/sql would normally own and close cc).
		if closer, ok := cc.(*connector); ok {
			_ = closer.Close()
		}
		return nil, err
	}
	// On the sql.Open path, database/sql keeps the connector and calls
	// connector.Close() on db.Close(). A direct Driver.Open caller discards the
	// connector, so tie the keeper session's lifetime to the returned
	// connection: closing the conn also releases the keeper.
	if cn, ok := c.(*conn); ok {
		if cnr, ok := cc.(*connector); ok {
			cn.connector = cnr
		}
	}
	return c, nil
}

// OpenConnector expects the same format as driver.Open
func (d Driver) OpenConnector(name string) (driver.Connector, error) {
	opts, err := parseConnectStr(name)
	if err != nil {
		return nil, err
	}
	return NewConnect(opts)
}

type conn struct {
	udfPath     string
	driverType  DriverType
	bufferSize  int
	useUnsafe   bool
	isStreaming bool
	session     *chdb.Session
	// connector is set only on the legacy Driver.Open path (not the sql.Open
	// path, where database/sql owns and closes the connector). When set, Close
	// also releases the connector's keeper session.
	connector *connector
	tx        *tx

	QueryFun  queryHandle
	streamFun queryStream
}

// WithArrowSession exposes this pooled connection's Session only for the
// duration of a database/sql Conn.Raw callback. Callers must finish all Arrow
// work inside fn and must not retain or close the Session themselves.
func (c *conn) WithArrowSession(fn func(*chdb.Session) error) error {
	if c.session == nil || fn == nil {
		return fmt.Errorf("chdb: closed connection or nil Arrow callback")
	}
	return fn(c.session)
}

func prepareValues(values []driver.Value) []driver.NamedValue {
	namedValues := make([]driver.NamedValue, len(values))
	for i, value := range values {
		namedValues[i] = driver.NamedValue{
			// nb: Name field is optional
			Ordinal: i,
			Value:   value,
		}
	}
	return namedValues
}

func (c *conn) Close() error {
	if c.tx != nil {
		c.tx.end()
	}
	if c.session != nil {
		c.session.Close()
		c.session = nil
	}
	// Only set on the Driver.Open path; releases the keeper that pins the data
	// path/temp dir for the connector.
	if c.connector != nil {
		_ = c.connector.Close()
		c.connector = nil
	}
	return nil
}

// SetupQueryFun wires both entry points, streaming or not. Exec never streams —
// it throws the result away — so it goes through QueryFun even on a streaming
// connection, where QueryFun used to be left nil and turned every db.Exec into a
// nil-func call.
func (c *conn) SetupQueryFun() {
	c.QueryFun = chdb.Query
	c.streamFun = chdb.QueryStream

	if c.session != nil {
		c.QueryFun = c.session.Query
		c.streamFun = c.session.QueryStream
	}
}

func (c *conn) Query(query string, values []driver.Value) (driver.Rows, error) {
	return c.QueryContext(context.Background(), query, prepareValues(values))
}

func (c *conn) QueryRow(query string, values []driver.Value) *singleRow {
	return c.QueryRowContext(context.Background(), query, values)
}

func (c *conn) Exec(query string, values []driver.Value) (sql.Result, error) {
	return c.ExecContext(context.Background(), query, prepareValues(values))
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	params, named, err := namedParameters(args)
	if err != nil {
		return nil, err
	}
	var result chdbpurego.ChdbResult
	if named {
		if c.session == nil {
			return nil, fmt.Errorf("chdb: named binding requires a session")
		}
		result, err = c.session.QueryParams(query, c.driverType.GetFormat(), params)
	} else {
		compiledQuery, compileErr := c.compileArguments(query, args)
		if compileErr != nil {
			return nil, compileErr
		}
		result, err = c.QueryFun(compiledQuery, c.driverType.GetFormat(), c.udfPath)
	}
	if err != nil {
		return nil, err
	}
	res := &execResult{
		err:      nil,
		localRes: result,
	}
	runtime.SetFinalizer(res, func(r *execResult) {
		if r.localRes != nil {
			r.localRes.Free()
		}
	})
	return res, nil
}

func (c *conn) QueryRowContext(ctx context.Context, query string, values []driver.Value) *singleRow {

	v, err := c.QueryContext(ctx, query, prepareValues(values))
	if err != nil {
		return &singleRow{
			err:  err,
			rows: nil,
		}
	}
	return &singleRow{
		rows: v,
	}
}

// CheckNamedValue lets unsigned integers through untouched. Arguments are
// interpolated into the query text, which formats uint64 exactly, but
// database/sql's default converter refuses a uint64 with the high bit set,
// and UInt64 columns hold values up to math.MaxUint64. Every other type keeps
// the default conversion.
func (c *conn) CheckNamedValue(nv *driver.NamedValue) error {
	switch nv.Value.(type) {
	case uint, uint64:
		return nil
	}
	return driver.ErrSkip
}

// compileArguments replaces each placeholder with its encoded argument. The
// number of arguments must match the number of placeholders. A query with no
// arguments is returned untouched, because ? is also ClickHouse's ternary
// operator.
func (c *conn) compileArguments(query string, args []driver.NamedValue) (string, error) {
	if len(args) == 0 {
		return query, nil
	}
	pos := placeholders(query)
	if len(pos) != len(args) {
		return "", fmt.Errorf("chdb: the query has %d placeholders but %d arguments were given", len(pos), len(args))
	}
	var b strings.Builder
	last := 0
	for i, p := range pos {
		v, err := sqlbuilder.ClickHouse.Interpolate("?", []interface{}{args[i].Value})
		if err != nil {
			return "", err
		}
		b.WriteString(query[last:p])
		b.WriteString(v)
		last = p + 1
	}
	b.WriteString(query[last:])
	return b.String(), nil
}

// placeholders returns the byte offsets of the ? placeholders in query.
func placeholders(query string) []int {
	pos, _ := scanQuery(query)
	return pos
}

// stripComments replaces every comment in query with a space.
func stripComments(query string) string {
	_, comments := scanQuery(query)
	if len(comments) == 0 {
		return query
	}
	var b strings.Builder
	last := 0
	for _, c := range comments {
		b.WriteString(query[last:c[0]])
		b.WriteByte(' ')
		last = c[1]
	}
	b.WriteString(query[last:])
	return b.String()
}

// scanQuery returns the byte offsets of the ? placeholders in query and the
// [start, end) ranges of its comments. It splits the query into tokens with
// the rules of ClickHouse's lexer (src/Parsers/Lexer.cpp), so a ? or a comment
// marker is recognized exactly where the server would see one, and never
// inside a '...' string, a "..." or `...` identifier, a ‘...’ or “...”
// literal, an x'..' or b'..' string, a $tag$...$tag$ heredoc or a comment.
// Comments are --, // and "# " or "#!" through the end of the line (the
// newline is not part of the comment), and /* */, which nests. A string,
// identifier or comment that is not closed runs to the end of the query.
func scanQuery(query string) (placeholders []int, comments [][2]int) {
	n := len(query)
	prev := tokenOther // the last token that is not whitespace or a comment
	for i := 0; i < n; {
		start := i
		ch := query[i]
		kind := tokenOther
		switch {
		case isSQLWhitespace(ch):
			i++
			continue
		case isDigit(ch):
			i, kind = lexNumber(query, i, prev == tokenDot)
		case ch == '\'':
			i = lexQuoted(query, i)
		case ch == '"' || ch == '`':
			i, kind = lexQuoted(query, i), tokenQuotedIdentifier
		case ch == ')' || ch == ']':
			i, kind = i+1, tokenClosingBracket
		case ch == '.':
			if i > 0 && (i+1 >= n || !isDigit(query[i+1]) || prev == tokenClosingBracket ||
				prev == tokenBareWord || prev == tokenQuotedIdentifier || prev == tokenNumber) {
				i, kind = i+1, tokenDot
			} else {
				i, kind = lexFraction(query, i+1), tokenNumber
			}
		case ch == '-' && i+1 < n && query[i+1] == '-',
			ch == '/' && i+1 < n && query[i+1] == '/',
			ch == '#' && i+1 < n && (query[i+1] == ' ' || query[i+1] == '!'):
			if j := strings.IndexByte(query[i:], '\n'); j >= 0 {
				i += j
			} else {
				i = n
			}
			comments = append(comments, [2]int{start, i})
			continue
		case ch == '/' && i+1 < n && query[i+1] == '*':
			i = lexBlockComment(query, i)
			comments = append(comments, [2]int{start, i})
			continue
		case ch == '?':
			placeholders = append(placeholders, i)
			i++
		case ch == '\xE2' && i+5 < n && query[i+1] == '\x80' && (query[i+2] == '\x98' || query[i+2] == '\x9C'):
			if query[i+2] == '\x9C' {
				kind = tokenQuotedIdentifier
			}
			closing := string([]byte{'\xE2', '\x80', query[i+2] + 1})
			if j := strings.Index(query[i+3:], closing); j >= 0 {
				i += 3 + j + len(closing)
			} else {
				i = n
			}
		case ch == '\xE2' && i+3 <= n && query[i+1] == '\x88' && query[i+2] == '\x92': // − (minus sign)
			i += 3
		case ch == '$':
			if end, ok := lexHeredoc(query, i); ok {
				i = end
			} else if i+1 == n || !isWordChar(query[i+1]) {
				i++
			} else {
				i, kind = lexBareWord(query, i), tokenBareWord
			}
		case (ch == 'x' || ch == 'X' || ch == 'b' || ch == 'B') && i+2 < n && query[i+1] == '\'':
			i = lexHexOrBinString(query, i)
		case isWordChar(ch):
			i, kind = lexBareWord(query, i), tokenBareWord
		case ch >= 0x80:
			// Any other non-ASCII character is Unicode whitespace or an error
			// that fails the query, so skipping it like whitespace is enough.
			for i++; i < n && query[i]&0xC0 == 0x80; i++ {
			}
			continue
		default:
			i++
		}
		prev = kind
	}
	return placeholders, comments
}

// The token types scanQuery tells apart: a dot is tuple access or the start
// of a number depending on the token before it.
const (
	tokenOther = iota
	tokenBareWord
	tokenNumber
	tokenQuotedIdentifier
	tokenClosingBracket
	tokenDot
)

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHexDigit(c byte) bool {
	return isDigit(c) || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func isWordChar(c byte) bool {
	return isDigit(c) || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

func isSQLWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// lexQuoted returns the end of the '...', "..." or `...` token at i. A quote
// is escaped by a backslash or by doubling it.
func lexQuoted(query string, i int) int {
	quote := query[i]
	for i++; i < len(query); i++ {
		switch query[i] {
		case '\\':
			i++
		case quote:
			if i+1 < len(query) && query[i+1] == quote {
				i++
			} else {
				return i + 1
			}
		}
	}
	return len(query)
}

// lexHeredoc returns the end of the $tag$...$tag$ heredoc at i. The tag is
// made of word characters and may be empty.
func lexHeredoc(query string, i int) (int, bool) {
	j := strings.IndexByte(query[i+1:], '$')
	if j < 0 {
		return 0, false
	}
	for k := i + 1; k < i+1+j; k++ {
		if !isWordChar(query[k]) {
			return 0, false
		}
	}
	tag := query[i : i+j+2]
	k := strings.Index(query[i+len(tag):], tag)
	if k < 0 {
		return 0, false
	}
	return i + len(tag) + k + len(tag), true
}

func lexBareWord(query string, i int) int {
	for i++; i < len(query) && (isWordChar(query[i]) || query[i] == '$'); i++ {
	}
	return i
}

// lexHexOrBinString returns the end of the x'..' or b'..' string at i. Any
// character other than a digit of the base leaves it unclosed.
func lexHexOrBinString(query string, i int) int {
	hex := query[i] == 'x' || query[i] == 'X'
	for i += 2; i < len(query) && (hex && isHexDigit(query[i]) || !hex && (query[i] == '0' || query[i] == '1')); i++ {
	}
	if i >= len(query) || query[i] != '\'' {
		return len(query)
	}
	return i + 1
}

// lexBlockComment returns the end of the /* */ comment at i, counting nested
// comments.
func lexBlockComment(query string, i int) int {
	depth := 1
	for i += 2; i+2 <= len(query); {
		switch {
		case query[i] == '/' && query[i+1] == '*':
			i += 2
			depth++
		case query[i] == '*' && query[i+1] == '/':
			i += 2
			if depth--; depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(query)
}

// lexNumber returns the end and type of the token that starts with a digit at
// i: a number such as 1_000, 0x1F, 0b101, 1.5e-3 or 0x1p+4, or a bare word
// such as 1st. Right after a dot only the digits are part of the number, so
// that x.1.2 is two tuple accesses.
func lexNumber(query string, i int, afterDot bool) (int, int) {
	n := len(query)
	start := i
	if afterDot {
		i = lexDigits(query, i+1, false, false)
	} else {
		hex, startOfBlock := false, false
		if i+2 < n && query[i] == '0' && (query[i+1] == 'x' || query[i+1] == 'X') && isHexDigit(query[i+2]) {
			hex, startOfBlock, i = true, true, i+2
		} else if i+2 < n && query[i] == '0' && (query[i+1] == 'b' || query[i+1] == 'B') && (query[i+2] == '0' || query[i+2] == '1') {
			startOfBlock, i = true, i+2
		} else {
			i++
		}
		i = lexDigits(query, i, hex, startOfBlock)
		if i < n && query[i] == '.' {
			i = lexDigits(query, i+1, hex, true)
		}
		if i+1 < n && (hex && (query[i] == 'p' || query[i] == 'P') || !hex && (query[i] == 'e' || query[i] == 'E')) {
			i = lexExponent(query, i+1)
		}
	}
	if i >= n || !isWordChar(query[i]) {
		return i, tokenNumber
	}
	for i++; i < n && isWordChar(query[i]); i++ {
	}
	for k := start; k < i; k++ {
		if !isWordChar(query[k]) {
			return i, tokenOther // a malformed number such as 1.5x
		}
	}
	return i, tokenBareWord
}

// lexFraction returns the end of the number that starts with a dot, with i
// just after the dot, as in .5 or .5e-3.
func lexFraction(query string, i int) int {
	i = lexDigits(query, i, false, true)
	if i+1 < len(query) && (query[i] == 'e' || query[i] == 'E') {
		i = lexExponent(query, i+1)
	}
	return i
}

// lexExponent returns the end of an exponent, with i just after the e or p.
// The sign is taken even when no digit follows it, as ClickHouse does.
func lexExponent(query string, i int) int {
	if i+1 < len(query) && (query[i] == '-' || query[i] == '+') {
		i++
	}
	return lexDigits(query, i, false, true)
}

// lexDigits returns the end of the digits at i. An underscore separates two
// digits, but cannot start a block of them.
func lexDigits(query string, i int, hex, startOfBlock bool) int {
	isDigitOf := isDigit
	if hex {
		isDigitOf = isHexDigit
	}
	for ; i < len(query); i++ {
		if !isDigitOf(query[i]) && (query[i] != '_' || startOfBlock || i+1 >= len(query) || !isDigitOf(query[i+1])) {
			break
		}
		startOfBlock = false
	}
	return i
}

// CheckNamedValue lets database/sql pass sql.Named values to QueryContext and
// ExecContext without converting them to positional driver.Values first.
func (c *conn) CheckNamedValue(value *driver.NamedValue) error {
	if value.Name == "" {
		return driver.ErrSkip
	}
	_, err := parameterValue(value.Value)
	return err
}

func parameterValue(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case int, int8, int16, int32:
		return fmt.Sprint(v), nil
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(v), nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(v), 'g', -1, 32), nil
	case bool:
		if v {
			return "1", nil
		}
		return "0", nil
	case time.Time:
		return v.UTC().Format("2006-01-02 15:04:05.999999999"), nil
	default:
		return "", fmt.Errorf("chdb: unsupported native parameter value %T", value)
	}
}

func namedParameters(args []driver.NamedValue) ([]chdb.QueryParameter, bool, error) {
	if len(args) == 0 {
		return nil, false, nil
	}
	if args[0].Name == "" {
		for _, arg := range args {
			if arg.Name != "" {
				return nil, false, fmt.Errorf("chdb: cannot mix named and positional parameters")
			}
		}
		return nil, false, nil
	}
	params := make([]chdb.QueryParameter, len(args))
	seen := make(map[string]bool, len(args))
	for i, arg := range args {
		if arg.Name == "" || !validParameterName(arg.Name) || seen[arg.Name] {
			return nil, false, fmt.Errorf("chdb: invalid, duplicate or mixed parameter name %q", arg.Name)
		}
		seen[arg.Name] = true
		value, err := parameterValue(arg.Value)
		if err != nil {
			return nil, false, err
		}
		params[i] = chdb.QueryParameter{Name: arg.Name, Value: value}
	}
	return params, true, nil
}

func validParameterName(name string) bool {
	if name == "" {
		return false
	}
	for i, ch := range name {
		if ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || i > 0 && ch >= '0' && ch <= '9' {
			continue
		}
		return false
	}
	return true
}

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	params, named, err := namedParameters(args)
	if err != nil {
		return nil, err
	}
	compiledQuery := query
	if !named {
		compiledQuery, err = c.compileArguments(query, args)
		if err != nil {
			return nil, err
		}
	}
	if c.isStreaming {
		var result chdbpurego.ChdbStreamResult
		if named {
			if c.session == nil {
				return nil, fmt.Errorf("chdb: named binding requires a session")
			}
			result, err = c.session.QueryStreamParams(query, c.driverType.GetFormat(), params)
		} else {
			result, err = c.streamFun(compiledQuery, c.driverType.GetFormat(), c.udfPath)
		}
		if err != nil {
			return nil, err
		}
		rows, err := c.driverType.PrepareStreamingRows(result, c.bufferSize, c.useUnsafe)
		if err != nil {
			// There is no Rows to close the stream from, and the stream holds the
			// connection it was started on open — a sessionless query keeps a whole
			// session for it — so release it here instead of at the next GC.
			result.Free()
			return nil, err
		}
		return rows, nil
	}
	var result chdbpurego.ChdbResult
	if named {
		if c.session == nil {
			return nil, fmt.Errorf("chdb: named binding requires a session")
		}
		result, err = c.session.QueryParams(query, c.driverType.GetFormat(), params)
	} else {
		result, err = c.QueryFun(compiledQuery, c.driverType.GetFormat(), c.udfPath)
	}
	if err != nil {
		return nil, err
	}

	buf := result.Buf()
	if len(buf) == 0 {
		// Statements with no result set (DDL, SET, INSERT) land here. Nothing will
		// read the result, so free it instead of leaving it to no one.
		result.Free()
		return nil, fmt.Errorf("result is nil")
	}
	rows, err := c.driverType.PrepareRows(result, buf, c.bufferSize, c.useUnsafe)
	if err != nil {
		result.Free()
		return nil, err
	}
	return rows, nil

}

func (c *conn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

// BeginTx starts a batch, not an ACID transaction, with the semantics
// clickhouse-go gives database/sql: Exec on a prepared INSERT collects one row,
// Commit sends the rows collected for each INSERT target as a single INSERT,
// and Rollback discards them. Anything else, including db.Exec-style calls on
// the transaction, runs immediately and is not undone by Rollback.
func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if opts.ReadOnly || sql.IsolationLevel(opts.Isolation) != sql.LevelDefault {
		return nil, errors.New("chdb: isolation levels and read-only transactions are not supported")
	}
	if c.tx != nil {
		return nil, errors.New("chdb: a transaction is already open on this connection")
	}
	c.tx = &tx{c: c}
	return c.tx, nil
}

func (c *conn) Prepare(query string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), query)
}

// PrepareContext returns a client-side statement: chDB has no server-side
// prepare, so the query is kept and its arguments are interpolated on every
// execution, exactly as db.Exec and db.Query do.
//
// database/sql caches a driver statement per connection and hands the same one
// to code inside and outside a transaction (tx.Stmt reuses it), so whether an
// INSERT is batched is decided on every Exec from the connection's current
// state, never here.
func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	s := &stmt{c: c, query: query}
	if m := insertBatchQuery.FindStringSubmatch(stripComments(query)); m != nil {
		s.insert = &insertTarget{prefix: m[1], row: strings.TrimSpace(m[2])}
	}
	return s, nil
}

// insertBatchQuery matches "INSERT INTO [TABLE] t [(cols)] [VALUES row] [;]",
// with comments already removed, and captures the part before VALUES and the
// row template after it. INSERT ... SELECT, FORMAT and SETTINGS do not match
// and run as ordinary statements.
var insertBatchQuery = regexp.MustCompile(`(?is)^\s*(INSERT\s+INTO\s+(?:TABLE\s+)?[^\s(;]+(?:\s*\([^()]*\))?)(?:\s+VALUES\s*(.*?))?\s*;?\s*$`)

// insertTarget is a prepared INSERT split into the part before VALUES and the
// row template after it. An empty template takes one placeholder per argument,
// so "INSERT INTO t" accepts a row of any width.
type insertTarget struct {
	prefix string
	row    string
}

func (it *insertTarget) compileRow(c *conn, args []driver.NamedValue) (string, error) {
	row := it.row
	if row == "" {
		if len(args) == 0 {
			return "", errors.New("chdb: an INSERT without VALUES needs one argument per column")
		}
		row = "(" + strings.TrimSuffix(strings.Repeat("?, ", len(args)), ", ") + ")"
	}
	return c.compileArguments(row, args)
}

type tx struct {
	c       *conn
	batches []*batch
}

// batch holds the rows collected for one INSERT target.
type batch struct {
	prefix string
	rows   []string
}

func (t *tx) add(prefix, row string) {
	for _, b := range t.batches {
		if b.prefix == prefix {
			b.rows = append(b.rows, row)
			return
		}
	}
	t.batches = append(t.batches, &batch{prefix: prefix, rows: []string{row}})
}

func (t *tx) end() {
	if t.c != nil && t.c.tx == t {
		t.c.tx = nil
	}
	t.c = nil
}

// Commit sends the batches in the order their first row was added. Each batch
// is one INSERT, so a failure leaves the batches before it inserted.
func (t *tx) Commit() error {
	c, batches := t.c, t.batches
	if c == nil {
		return errors.New("chdb: commit after the connection was closed; batched rows were discarded")
	}
	t.end()
	for _, b := range batches {
		if _, err := c.ExecContext(context.Background(), b.prefix+" VALUES "+strings.Join(b.rows, ", "), nil); err != nil {
			return err
		}
	}
	return nil
}

func (t *tx) Rollback() error {
	t.end()
	return nil
}

type stmt struct {
	c      *conn
	query  string
	insert *insertTarget // set when the query is an INSERT that can be batched
}

func (s *stmt) Close() error { return nil }

// NumInput returns -1: compileArguments checks the argument count itself, and
// a statement executed without arguments keeps ? as the ternary operator.
func (s *stmt) NumInput() int { return -1 }

func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), prepareValues(args))
}

func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), prepareValues(args))
}

func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if s.insert == nil {
		return s.c.ExecContext(ctx, s.query, args)
	}
	row, err := s.insert.compileRow(s.c, args)
	if err != nil {
		return nil, err
	}
	if s.c.tx != nil {
		s.c.tx.add(s.insert.prefix, row)
		return driver.RowsAffected(0), nil
	}
	return s.c.ExecContext(ctx, s.insert.prefix+" VALUES "+row, nil)
}

func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.c.QueryContext(ctx, s.query, args)
}
