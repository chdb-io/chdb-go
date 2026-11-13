package chdbpurego

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/chdb-io/chdb-go/v2/internal/nativearrow"
)

// ErrArrowABIUnavailable leaves existing byte-format queries usable on old
// engines. The optional Arrow module must additionally check the behavior of
// the selected engine release before publishing its own compatibility range.
var ErrArrowABIUnavailable = errors.New("chdb: loaded libchdb lacks the required Arrow ABI symbols")

func (c *connection) Capabilities() nativearrow.Capabilities {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out nativearrow.Capabilities
	if c.conn == nil {
		return out
	}
	if chdbVersion != nil {
		out.EngineVersion = chdbVersion()
	}
	out.Input = chdbArrowScan != nil && chdbArrowUnregisterTable != nil
	out.TableOutput = chdbQueryArrowN != nil
	out.ReaderOutput = chdbStreamQueryArrowN != nil && chdbStreamFetchArrow != nil && chdbStreamCancelQuery != nil
	out.ParamBinding = chdbStreamQueryArrowWithParamsN != nil
	return out
}

func (c *connection) RegisterStream(name string, stream unsafe.Pointer) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.conn == nil {
		return errors.New("chdb: closed connection")
	}
	if chdbArrowScan == nil || chdbArrowUnregisterTable == nil {
		return ErrArrowABIUnavailable
	}
	if name == "" || stream == nil {
		return errors.New("chdb: invalid Arrow registration")
	}
	if chdbArrowScan(c.conn.internal_data, name, stream) != 0 {
		return fmt.Errorf("chdb: Arrow registration failed for %q", name)
	}
	return nil
}

func (c *connection) UnregisterStream(name string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.conn == nil {
		return errors.New("chdb: closed connection")
	}
	if chdbArrowUnregisterTable == nil {
		return ErrArrowABIUnavailable
	}
	if chdbArrowUnregisterTable(c.conn.internal_data, name) != 0 {
		return fmt.Errorf("chdb: Arrow unregister failed for %q", name)
	}
	return nil
}

func (c *connection) QueryBytes(sql, format string) ([]byte, error) {
	result, err := c.Query(sql, format)
	if err != nil {
		return nil, err
	}
	defer result.Free()
	return append([]byte(nil), result.Buf()...), nil
}

// QueryArrow transfers an Arrow C Stream into caller-allocated native memory.
// The caller must release the stream callback independently of this result.
func (c *connection) QueryArrow(sql string, outStream unsafe.Pointer, options *nativearrow.OutputOptions) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.conn == nil {
		return errors.New("chdb: closed connection")
	}
	if chdbQueryArrowN == nil {
		return ErrArrowABIUnavailable
	}
	if outStream == nil {
		return errors.New("chdb: nil Arrow output stream")
	}
	query := []byte(sql)
	if len(query) == 0 {
		return errors.New("chdb: empty Arrow query")
	}
	res := chdbQueryArrowN(c.conn.internal_data, &query[0], uint(len(query)), outStream, unsafe.Pointer(options))
	runtime.KeepAlive(options)
	if res == nil {
		return errors.New("chdb: Arrow query returned no result handle")
	}
	defer chdbDestroyQueryResult(res)
	if msg := chdbResultError(res); msg != "" {
		return errors.New(msg)
	}
	return nil
}

type arrowStream struct {
	mu     sync.RWMutex // native handle survives concurrent fetch/close
	conn   *connection
	result *chdb_result
}

var _ nativearrow.Stream = (*arrowStream)(nil)

func (c *connection) QueryArrowStream(sql string, options *nativearrow.OutputOptions) (nativearrow.Stream, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.conn == nil {
		return nil, errors.New("chdb: closed connection")
	}
	if chdbStreamQueryArrowN == nil || chdbStreamFetchArrow == nil {
		return nil, ErrArrowABIUnavailable
	}
	query := []byte(sql)
	if len(query) == 0 {
		return nil, errors.New("chdb: empty streaming Arrow query")
	}
	result := chdbStreamQueryArrowN(c.conn.internal_data, &query[0], uint(len(query)), unsafe.Pointer(options))
	runtime.KeepAlive(options)
	if result == nil {
		return nil, errors.New("chdb: Arrow stream init returned no result handle")
	}
	if msg := chdbResultError(result); msg != "" {
		chdbDestroyQueryResult(result)
		return nil, errors.New(msg)
	}
	return &arrowStream{conn: c, result: result}, nil
}

// QueryArrowStreamParams uses the engine's binary-safe per-call binding ABI.
// The parameter buffers and pointer arrays are pinned only for the native
// initialization call; core captures their values before it returns.
func (c *connection) QueryArrowStreamParams(sql string, options *nativearrow.OutputOptions, params []nativearrow.Parameter) (nativearrow.Stream, error) {
	if len(params) == 0 {
		return c.QueryArrowStream(sql, options)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.conn == nil {
		return nil, errors.New("chdb: closed connection")
	}
	if chdbStreamQueryArrowWithParamsN == nil || chdbStreamFetchArrow == nil {
		return nil, ErrArrowABIUnavailable
	}
	if sql == "" {
		return nil, errors.New("chdb: empty streaming Arrow query")
	}
	query := []byte(sql)
	result := pinnedParams(params, func(names **byte, nameLens *uint, values **byte, valueLens *uint, count uint) *chdb_result {
		return chdbStreamQueryArrowWithParamsN(c.conn.internal_data, &query[0], uint(len(query)), unsafe.Pointer(options),
			names, nameLens, values, valueLens, count)
	})
	runtime.KeepAlive(query)
	runtime.KeepAlive(options)
	if result == nil {
		return nil, errors.New("chdb: Arrow stream init returned no result handle")
	}
	if msg := chdbResultError(result); msg != "" {
		chdbDestroyQueryResult(result)
		return nil, errors.New(msg)
	}
	return &arrowStream{conn: c, result: result}, nil
}

func (s *arrowStream) Fetch(out unsafe.Pointer) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.result == nil || out == nil {
		return errors.New("chdb: closed Arrow stream or nil output")
	}
	s.conn.mu.RLock()
	defer s.conn.mu.RUnlock()
	if s.conn.conn == nil {
		return errors.New("chdb: closed connection")
	}
	if chdbStreamFetchArrow(s.conn.conn.internal_data, s.result, out) != 0 {
		if msg := chdbResultError(s.result); msg != "" {
			return errors.New(msg)
		}
		return errors.New("chdb: Arrow stream fetch failed")
	}
	return nil
}

func (s *arrowStream) Close(cancel bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.result == nil {
		return
	}
	s.conn.mu.RLock()
	if s.conn.conn != nil {
		if cancel {
			s.conn.cancelStreamLocked(s.result)
		}
		chdbDestroyQueryResult(s.result)
	}
	s.conn.mu.RUnlock()
	s.result = nil
}
