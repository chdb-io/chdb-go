#pragma once

#ifdef __cplusplus
#include <cstddef>
#include <cstdint>
extern "C" {
#else
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#endif

#define CHDB_EXPORT __attribute__((visibility("default")))

#define CHDB_VERSION "26.7.3"

/**
 * Returns the version of the linked chDB library.
 * @return Null-terminated version string, e.g. "26.7.2"
 */
CHDB_EXPORT const char * chdb_version(void);

#ifndef CHDB_NO_DEPRECATED
// WARNING: The following structs are deprecated and will be removed in a future version.
struct local_result
{
    char * buf;
    size_t len;
    void * _vec; // std::vector<char> *, for freeing
    double elapsed;
    uint64_t rows_read;
    uint64_t bytes_read;
};

#ifdef __cplusplus
struct local_result_v2
{
    char * buf = nullptr;
    size_t len = 0;
    void * _vec = nullptr; // std::vector<char> *, for freeing
    double elapsed = 0.0;
    uint64_t rows_read = 0;
    uint64_t bytes_read = 0;
    char * error_message = nullptr;
};
#else
struct local_result_v2
{
    char * buf;
    size_t len;
    void * _vec; // std::vector<char> *, for freeing
    double elapsed;
    uint64_t rows_read;
    uint64_t bytes_read;
    char * error_message;
};
#endif

/**
 * Connection structure for chDB
 * Contains ChdbClient instance and connection state
 */
struct chdb_conn
{
    void * server; /* ChdbClient instance */
    bool connected; /* Connection state flag */
};

typedef struct
{
	void * internal_data;
} chdb_streaming_result;

#endif

// Return state enumeration for chDB API functions
typedef enum chdb_state
{
    CHDBSuccess = 0,
    CHDBError = 1
} chdb_state;

// What a statement does to state that outlives it, as decided by the
// ClickHouse parser -- see chdb_classify_query_n().
//
// The classes answer two questions a caller replaying statements has to keep
// apart: does this change anything that outlives the statement, and would
// `BACKUP DATABASE <db>` carry the change?
//
//                            outlives it?   `BACKUP DATABASE` carries it?
//   READ_ONLY                     no              --
//   MUTATING                      yes             yes
//   MUTATING_GLOBAL               yes             NO
//   CONTROL                    session only, or not replayable at all
//
// MUTATING_GLOBAL is the class that catches people out. `CREATE FUNCTION`,
// `CREATE USER` and a named collection all change something real and are
// worth replaying, but they live beside the databases rather than inside one,
// so a checkpoint of the database does not hold them. A caller that files
// them with the MUTATING statements and later checkpoints loses them without
// an error -- the failure surfaces much later, somewhere else. Keep them
// where a checkpoint cannot truncate them.
//
// Values ascend by how restricted the statement is, so a batch of statements
// classifies as the maximum over its members.
typedef enum chdb_query_class
{
    // SELECT, SHOW, DESCRIBE, EXPLAIN, EXISTS, CHECK: leaves no trace.
    CHDB_QUERY_READ_ONLY = 0,
    // INSERT, CREATE, ALTER, DROP, TRUNCATE, RENAME, UPDATE, DELETE, OPTIMIZE:
    // changes a database, and `BACKUP DATABASE` captures the change.
    CHDB_QUERY_MUTATING = 1,
    // Global UDFs, named collections, workloads, resources, access management,
    // and writes into the `system` database: persistent, replayable, and
    // outside every database a checkpoint could capture.
    CHDB_QUERY_MUTATING_GLOBAL = 2,
    // USE, SET, ATTACH, DETACH, SYSTEM, BACKUP, RESTORE, KILL, transaction
    // control, and statements writing outside the engine altogether
    // (INTO OUTFILE, INSERT INTO FUNCTION): either session-scoped, so
    // replaying makes no sense, or not something a caller should be issuing
    // through a managed connection at all.
    CHDB_QUERY_CONTROL = 3,
    // Did not parse, or parsed into a statement this version does not classify.
    // Callers gating writes on the class must treat it as a refusal.
    CHDB_QUERY_UNKNOWN = 4
} chdb_query_class;

// Facts about a statement beyond its class -- see chdb_classify_query_n().
typedef enum chdb_query_analysis_flag
{
    // The text carries a credential: a named collection's key, a password, an
    // access key handed to a table function. Never set when the class is
    // UNKNOWN, since nothing was proven about text that did not parse.
    CHDB_QUERY_HAS_SECRETS = 1u << 0,
    // Every persistent write the statement performs lands in the database the
    // caller named. Set only when that can be proven: an unqualified name is
    // resolved through the connection's current database first, and any write
    // to another database, to `system`, to a table function, or to a file
    // clears it. A statement that writes nothing sets it vacuously.
    CHDB_QUERY_WRITES_ONLY_TARGET_DATABASE = 1u << 1,
    // The statement creates, drops or renames a database rather than acting
    // inside one. That is a change to the container, which a caller managing
    // an object per database has to handle itself rather than log.
    CHDB_QUERY_CHANGES_DATABASE_LIFECYCLE = 1u << 2
} chdb_query_analysis_flag;

// What chdb_classify_query_n() reports. Versioned by size: set struct_size to
// sizeof the struct you compiled against, and a newer engine will fill only
// the fields you have room for.
typedef struct chdb_query_analysis_v1
{
    uint32_t struct_size;      // caller sets to sizeof(chdb_query_analysis_v1)
    uint32_t statement_count;  // executable statements; PARALLEL WITH arms count
    uint32_t flags;            // chdb_query_analysis_flag, OR-ed
    uint32_t query_class;      // chdb_query_class, as a fixed-width ABI field
} chdb_query_analysis_v1;

// Opaque handle for query results.
// Internal data structure managed by chDB implementation.
// Users should only interact through API functions.
typedef struct chdb_result_
{
	void * internal_data;
} chdb_result;

// Connection handle wrapping database session state.
// Internal data structure managed by chDB implementation.
// Users should only interact through API functions.
typedef struct chdb_connection_
{
	void * internal_data;
} * chdb_connection;

// Holds an arrow array stream.
typedef struct chdb_arrow_stream_
{
	void * internal_data;
} * chdb_arrow_stream;

// Holds an arrow schema.
typedef struct chdb_arrow_schema_
{
	void * internal_data;
} * chdb_arrow_schema;

// Holds an arrow array.
typedef struct chdb_arrow_array_
{
	void * internal_data;
} * chdb_arrow_array;

// Opaque handle for a streaming INSERT (chdb_stream_insert family).
// Internal data structure managed by chDB implementation.
// Users should only interact through API functions.
typedef struct chdb_insert_stream_
{
	void * internal_data;
} * chdb_insert_stream;

#ifndef CHDB_NO_DEPRECATED
// WARNING: The following interfaces are deprecated and will be removed in a future version.
CHDB_EXPORT struct local_result * query_stable(int argc, char ** argv);
CHDB_EXPORT void free_result(struct local_result * result);

CHDB_EXPORT struct local_result_v2 * query_stable_v2(int argc, char ** argv);
CHDB_EXPORT void free_result_v2(struct local_result_v2 * result);

/**
 * Creates a new chDB connection.
 *
 * The engine uses one storage path per process: multiple connections to that
 * same path may be open at once. Connecting with a different path requires
 * closing all existing connections first, which shuts the engine down and
 * boots a new one.
 *
 * Keep at least one connection open for as long as the host needs chDB. The
 * engine is meant to start once per process: repeatedly closing the last
 * connection and reconnecting puts it through a full shutdown and boot each
 * time, and doing that many times in one process is known to corrupt the
 * process allocator on macOS and abort -- either in the engine's own teardown
 * or at some unrelated allocation afterwards. A host that pools connections
 * should hold one outside the pool rather than let the pool empty and
 * reconnect on the next request: an idle timeout or a maximum-lifetime policy
 * that retires the last connection puts it on that path with no indication.
 *
 * Arguments naming a ClickHouse query-level setting (--<setting>=<value>,
 * e.g. --max_threads=4 or --output_format_json_quote_denormals=1) apply to
 * this connection only, exactly as if it had executed SET <setting> = <value>:
 * concurrent connections to the same path each keep their own settings.
 * An invalid value for a known setting fails the connection. Server-level
 * options (--path=..., --user_scripts_path=..., logging options, ...) are consumed by
 * the connection that boots the engine and ignored afterwards.
 *
 * @param argc Number of command-line arguments
 * @param argv Command-line arguments array (--path=<db_path> to specify database location)
 * @return Pointer to connection pointer, or NULL on failure
 * @note Default path is ":memory:" if not specified
 */
CHDB_EXPORT struct chdb_conn ** connect_chdb(int argc, char ** argv);

/**
 * Closes an existing chDB connection and cleans up resources.
 * Thread-safe function that handles connection shutdown and cleanup.
 *
 * Closing the last open connection shuts the engine down. See chdb_connect()
 * for why a host should not do that until it is finished with chDB.
 *
 * @param conn Pointer to connection pointer to close
 */
CHDB_EXPORT void close_conn(struct chdb_conn ** conn);

/**
 * Executes a query on the given connection.
 * Thread-safe function that handles query execution in a separate thread.
 *
 * @param conn Connection to execute query on
 * @param query SQL query string to execute
 * @param format Output format string (e.g., "CSV", default format)
 * @return Query result structure containing output or error message
 * @note Returns error result if connection is invalid or closed
 */
CHDB_EXPORT struct local_result_v2 * query_conn(struct chdb_conn * conn, const char * query, const char * format);

/**
 * Executes a query on the given connection with explicit length parameters.
 * @brief Thread-safe query execution with binary-safe string handling
 * @param conn Connection to execute query on
 * @param query SQL query string to execute (may contain null bytes)
 * @param query_len Length of query string in bytes
 * @param format Output format string (e.g., "CSV", default format)
 * @param format_len Length of format string in bytes
 * @return Query result structure containing output or error message
 * @note Returns error result if connection is invalid or closed
 * @note This function is binary-safe and can handle queries containing null bytes
 */
CHDB_EXPORT struct local_result_v2 *
query_conn_n(struct chdb_conn * conn, const char * query, size_t query_len, const char * format, size_t format_len);

/**
 * Executes a streaming query on the given connection.
 * @brief Initializes streaming query execution and returns result handle
 * @param conn Connection to execute query on
 * @param query SQL query string to execute
 * @param format Output format string (e.g. "CSV", default format)
 * @return Streaming result handle containing query state or error message
 * @note Returns error result if connection is invalid or closed
 */
CHDB_EXPORT chdb_streaming_result * query_conn_streaming(struct chdb_conn * conn, const char * query, const char * format);

/**
 * Executes a streaming query on the given connection with explicit length parameters.
 * @brief Initializes streaming query execution with binary-safe string handling
 * @param conn Connection to execute query on
 * @param query SQL query string to execute (may contain null bytes)
 * @param query_len Length of query string in bytes
 * @param format Output format string (e.g., "CSV", default format)
 * @param format_len Length of format string in bytes
 * @return Streaming result handle containing query state or error message
 * @note Returns error result if connection is invalid or closed
 * @note This function is binary-safe and can handle queries containing null bytes
 * @note Use chdb_streaming_fetch_result() to retrieve data chunks from the streaming query
 */
CHDB_EXPORT chdb_streaming_result *
query_conn_streaming_n(struct chdb_conn * conn, const char * query, size_t query_len, const char * format, size_t format_len);

/**
 * Retrieves error message from streaming result.
 * @brief Gets error message associated with streaming query execution
 * @param result Streaming result handle from query_conn_streaming()
 * @return Null-terminated error message string, or NULL if no error occurred
 */
CHDB_EXPORT const char * chdb_streaming_result_error(chdb_streaming_result * result);

/**
 * Fetches next chunk of streaming results.
 * @brief Iterates through streaming query results
 * @param conn Active connection handle
 * @param result Streaming result handle from query_conn_streaming()
 * @return Materialized result chunk with data
 * @note Returns empty result when stream ends
 * @note Fetching past the natural end of the stream is safe and idempotent:
 *       every further call returns a fresh empty, non-error result. Fetching
 *       after chdb_streaming_cancel_query() or after a mid-stream error still
 *       returns an error result.
 * @note Not thread-safe per connection: a connection runs one statement at a
 *       time, so fetches (and cancel) on a streaming handle must be serialized
 *       by the caller — do not fetch the same stream from multiple threads
 *       concurrently.
 */
CHDB_EXPORT struct local_result_v2 * chdb_streaming_fetch_result(struct chdb_conn * conn, chdb_streaming_result * result);

/**
 * Cancels ongoing streaming query.
 * @brief Aborts streaming query execution and cleans up resources
 * @param conn Active connection handle
 * @param result Streaming result handle to cancel
 */
CHDB_EXPORT void chdb_streaming_cancel_query(struct chdb_conn * conn, chdb_streaming_result * result);

/**
 * Releases resources associated with streaming result.
 * @brief Destroys streaming result handle and frees allocated memory
 * @param result Streaming result handle to destroy
 * @warning Must be called even if query was finished or canceled
 */
 CHDB_EXPORT void chdb_destroy_result(chdb_streaming_result * result);

#endif

/**
 * Creates a new chDB connection.
 *
 * The engine uses one storage path per process: multiple connections to that
 * same path may be open at once. Connecting with a different path requires
 * closing all existing connections first, which shuts the engine down and
 * boots a new one.
 *
 * Keep at least one connection open for as long as the host needs chDB. The
 * engine is meant to start once per process: repeatedly closing the last
 * connection and reconnecting puts it through a full shutdown and boot each
 * time, and doing that many times in one process is known to corrupt the
 * process allocator on macOS and abort -- either in the engine's own teardown
 * or at some unrelated allocation afterwards. A host that pools connections
 * should hold one outside the pool rather than let the pool empty and
 * reconnect on the next request: an idle timeout or a maximum-lifetime policy
 * that retires the last connection puts it on that path with no indication.
 *
 * Arguments naming a ClickHouse query-level setting (--<setting>=<value>,
 * e.g. --max_threads=4 or --output_format_json_quote_denormals=1) apply to
 * this connection only, exactly as if it had executed SET <setting> = <value>:
 * concurrent connections to the same path each keep their own settings.
 * An invalid value for a known setting fails the connection. Server-level
 * options (--path=..., --user_scripts_path=..., logging options, ...) are consumed by
 * the connection that boots the engine and ignored afterwards.
 *
 * @param argc Number of command-line arguments
 * @param argv Command-line arguments array (--path=<db_path> to specify database location)
 * @return Pointer to connection pointer, or NULL on failure
 * @note Default path is ":memory:" if not specified
 */
CHDB_EXPORT chdb_connection * chdb_connect(int argc, char ** argv);

/**
 * Closes an existing chDB connection and cleans up resources.
 * Thread-safe function that handles connection shutdown and cleanup.
 *
 * Closing the last open connection shuts the engine down. See chdb_connect()
 * for why a host should not do that until it is finished with chDB.
 *
 * @param conn Pointer to connection pointer to close
 */
 CHDB_EXPORT void chdb_close_conn(chdb_connection * conn);

/**
 * Executes a query on the given connection.
 * Thread-safe function that handles query execution in a separate thread.
 *
 * @param conn Connection to execute query on
 * @param query SQL query string to execute
 * @param format Output format string (e.g., "CSV", default format)
 * @return Query result structure containing output or error message
 * @note Returns error result if connection is invalid or closed
 */
CHDB_EXPORT chdb_result * chdb_query(chdb_connection conn, const char * query, const char * format);

/**
 * Executes a query on the given connection with explicit length parameters.
 * @brief Thread-safe query execution with binary-safe string handling
 * @param conn Connection to execute query on
 * @param query SQL query string to execute (may contain null bytes)
 * @param query_len Length of query string in bytes
 * @param format Output format string (e.g., "CSV", default format)
 * @param format_len Length of format string in bytes
 * @return Query result structure containing output or error message
 * @note Returns error result if connection is invalid or closed
 * @note This function is binary-safe and can handle queries containing null bytes
 * @note Use chdb_result_* functions to access result data and metadata
 */
CHDB_EXPORT chdb_result * chdb_query_n(chdb_connection conn, const char * query, size_t query_len, const char * format, size_t format_len);

/**
 * @brief Execute a query with command-line interface
 * @param argc Argument count (same as main()'s argc)
 * @param argv Argument vector (same as main()'s argv)
 * @return Query result structure containing output or error message
 */
CHDB_EXPORT chdb_result * chdb_query_cmdline(int argc, char ** argv);

/**
 * Executes a streaming query on the given connection.
 * @brief Initializes streaming query execution and returns result handle
 * @param conn Connection to execute query on
 * @param query SQL query string to execute
 * @param format Output format string (e.g. "CSV", default format)
 * @return Streaming result handle containing query state or error message
 * @note Returns error result if connection is invalid or closed
 */
CHDB_EXPORT chdb_result * chdb_stream_query(chdb_connection conn, const char * query, const char * format);

/**
 * Executes a streaming query with explicit string lengths (binary-safe).
 * @brief Initializes streaming query execution with specified buffer lengths
 * @param conn Connection to execute query on
 * @param query SQL query buffer (may contain null bytes)
 * @param query_len Length of query buffer in bytes
 * @param format Output format buffer (may contain null bytes)
 * @param format_len Length of format buffer in bytes
 * @return Streaming result handle containing query state or error message
 * @note Strings do not need to be null-terminated
 * @note Use this function when dealing with queries/formats containing null bytes
 */
CHDB_EXPORT chdb_result *
chdb_stream_query_n(chdb_connection conn, const char * query, size_t query_len, const char * format, size_t format_len);

/**
 * Executes a query with server-side named parameter binding.
 * @brief Binds {name:Type} placeholders before query execution; values are NOT interpolated into SQL.
 * @param conn Connection to execute query on
 * @param query SQL query string (NUL-terminated, e.g. "SELECT {x:Int64} AS v")
 * @param format Output format string (e.g. "CSV", "JSON")
 * @param param_names Array of param_count NUL-terminated parameter names (must match {name:Type} placeholders)
 * @param param_values Array of param_count NUL-terminated parameter values
 * @param param_count Number of name/value pairs in the arrays
 * @return Query result structure containing output or error message
 * @note Parameter values are passed to the engine as strings; the engine resolves the type from the
 *       {name:Type} placeholder. This avoids SQL injection (no string interpolation) and enables
 *       server-side query-plan caching.
 * @note On duplicate parameter names, the last value wins (NameToNameMap semantics).
 * @note Parameters are scoped to this single call and cleared on return (RAII).
 * @note Use chdb_query_with_params_n for binary-safe values containing NUL bytes.
 */
CHDB_EXPORT chdb_result * chdb_query_with_params(
    chdb_connection conn,
    const char * query,
    const char * format,
    const char * const * param_names,
    const char * const * param_values,
    size_t param_count);

/**
 * Executes a query with server-side named parameter binding and explicit string lengths.
 * @brief Binary-safe variant of chdb_query_with_params() — values may contain NUL bytes.
 * @param conn Connection to execute query on
 * @param query SQL query buffer (may contain NUL bytes)
 * @param query_len Length of query buffer in bytes
 * @param format Output format buffer (may contain NUL bytes)
 * @param format_len Length of format buffer in bytes
 * @param param_names Array of param_count parameter names (each name_lens[i] bytes long)
 * @param param_name_lens Array of param_count byte lengths for param_names
 * @param param_values Array of param_count parameter value buffers (each value_lens[i] bytes long)
 * @param param_value_lens Array of param_count byte lengths for param_values
 * @param param_count Number of name/value pairs
 * @return Query result structure containing output or error message
 * @note Strings do not need to be NUL-terminated.
 */
CHDB_EXPORT chdb_result * chdb_query_with_params_n(
    chdb_connection conn,
    const char * query,
    size_t query_len,
    const char * format,
    size_t format_len,
    const char * const * param_names,
    const size_t * param_name_lens,
    const char * const * param_values,
    const size_t * param_value_lens,
    size_t param_count);

/**
 * Executes a streaming query with server-side named parameter binding.
 * @brief Initializes streaming query execution with parameter binding.
 * @param conn Connection to execute query on
 * @param query SQL query string (NUL-terminated)
 * @param format Output format string (e.g. "CSV", "JSON")
 * @param param_names Array of param_count NUL-terminated parameter names
 * @param param_values Array of param_count NUL-terminated parameter values
 * @param param_count Number of name/value pairs
 * @return Streaming result handle containing query state or error message
 * @note Parameters are bound on the connection before streaming starts and cleared once the
 *       streaming initialization returns (the engine has already captured the parameter values).
 */
CHDB_EXPORT chdb_result * chdb_stream_query_with_params(
    chdb_connection conn,
    const char * query,
    const char * format,
    const char * const * param_names,
    const char * const * param_values,
    size_t param_count);

/**
 * Executes a streaming query with server-side named parameter binding and explicit string lengths.
 * @brief Binary-safe variant of chdb_stream_query_with_params().
 * @param conn Connection to execute query on
 * @param query SQL query buffer (may contain NUL bytes)
 * @param query_len Length of query buffer in bytes
 * @param format Output format buffer (may contain NUL bytes)
 * @param format_len Length of format buffer in bytes
 * @param param_names Array of param_count parameter names
 * @param param_name_lens Array of param_count byte lengths for param_names
 * @param param_values Array of param_count parameter value buffers
 * @param param_value_lens Array of param_count byte lengths for param_values
 * @param param_count Number of name/value pairs
 * @return Streaming result handle containing query state or error message
 */
CHDB_EXPORT chdb_result * chdb_stream_query_with_params_n(
    chdb_connection conn,
    const char * query,
    size_t query_len,
    const char * format,
    size_t format_len,
    const char * const * param_names,
    const size_t * param_name_lens,
    const char * const * param_values,
    const size_t * param_value_lens,
    size_t param_count);

/**
 * Fetches next chunk of streaming results.
 * @brief Iterates through streaming query results
 * @param conn Active connection handle
 * @param result Streaming result handle from query_conn_streaming()
 * @return Materialized result chunk with data
 * @note Returns empty result when stream ends
 * @note Fetching past the natural end of the stream is safe and idempotent:
 *       every further call returns a fresh empty, non-error result. Fetching
 *       after chdb_stream_cancel_query() or after a mid-stream error still
 *       returns an error result.
 * @note Not thread-safe per connection: a connection runs one statement at a
 *       time, so fetches (and cancel) on a streaming handle must be serialized
 *       by the caller — do not fetch the same stream from multiple threads
 *       concurrently.
 */
CHDB_EXPORT chdb_result * chdb_stream_fetch_result(chdb_connection conn, chdb_result * result);

/**
 * Cancels ongoing streaming query.
 * @brief Aborts streaming query execution and cleans up resources
 * @param conn Active connection handle
 * @param result Streaming result handle to cancel
 */
CHDB_EXPORT void chdb_stream_cancel_query(chdb_connection conn, chdb_result * result);

/**
 * Destroys a query result and releases all associated resources
 * @param result The result handle to destroy
 */
CHDB_EXPORT void chdb_destroy_query_result(chdb_result * result);

//===--------------------------------------------------------------------===//
// Streaming INSERT (write side)
//===--------------------------------------------------------------------===//

/**
 * Begins a streaming INSERT on the given connection.
 * @brief Write-side counterpart of chdb_stream_query(): send the INSERT, then push data in chunks
 * @param conn Active connection handle
 * @param query INSERT statement without FORMAT clause or inline data (e.g., "INSERT INTO t (a, b)")
 * @param format Input format of the appended data (e.g., "CSV", "JSONEachRow", "Parquet")
 * @return Insert stream handle, never NULL; check chdb_stream_insert_error() for init failure
 * @note The connection accepts no other statement while the stream is open
 */
CHDB_EXPORT chdb_insert_stream chdb_stream_insert(chdb_connection conn, const char * query, const char * format);

/**
 * Begins a streaming INSERT with explicit string lengths (binary-safe).
 * @brief Variant of chdb_stream_insert() with specified buffer lengths
 * @param conn Active connection handle
 * @param query INSERT statement without FORMAT clause or inline data
 * @param query_len Length of the query string in bytes
 * @param format Input format of the appended data
 * @param format_len Length of the format string in bytes
 * @return Insert stream handle, never NULL; check chdb_stream_insert_error() for init failure
 */
CHDB_EXPORT chdb_insert_stream chdb_stream_insert_n(
    chdb_connection conn, const char * query, size_t query_len, const char * format, size_t format_len);

/**
 * Begins a streaming INSERT with server-side named parameter binding.
 * @brief Variant of chdb_stream_insert() for INSERT statements with {name:Type}
 *        placeholders, e.g. "INSERT INTO FUNCTION file({path:String}, {format:String}, {structure:String})"
 * @param conn Active connection handle
 * @param query INSERT statement without FORMAT clause or inline data
 * @param format Input format of the appended data (e.g., "CSV", "JSONEachRow", "Parquet")
 * @param param_names Array of param_count NUL-terminated parameter names
 * @param param_values Array of param_count NUL-terminated parameter values
 * @param param_count Number of name/value pairs
 * @return Insert stream handle, never NULL; check chdb_stream_insert_error() for init failure
 * @note Parameters are bound on the connection for the duration of stream
 *       initialization and cleared once it returns (the engine has already
 *       captured the parameter values).
 */
CHDB_EXPORT chdb_insert_stream chdb_stream_insert_with_params(
    chdb_connection conn,
    const char * query,
    const char * format,
    const char * const * param_names,
    const char * const * param_values,
    size_t param_count);

/**
 * Begins a streaming INSERT with named parameter binding and explicit string lengths.
 * @brief Binary-safe variant of chdb_stream_insert_with_params().
 * @param conn Active connection handle
 * @param query INSERT statement buffer (may contain NUL bytes)
 * @param query_len Length of query buffer in bytes
 * @param format Input format buffer
 * @param format_len Length of format buffer in bytes
 * @param param_names Array of param_count parameter names
 * @param param_name_lens Array of param_count byte lengths for param_names
 * @param param_values Array of param_count parameter value buffers
 * @param param_value_lens Array of param_count byte lengths for param_values
 * @param param_count Number of name/value pairs
 * @return Insert stream handle, never NULL; check chdb_stream_insert_error() for init failure
 */
CHDB_EXPORT chdb_insert_stream chdb_stream_insert_with_params_n(
    chdb_connection conn,
    const char * query,
    size_t query_len,
    const char * format,
    size_t format_len,
    const char * const * param_names,
    const size_t * param_name_lens,
    const char * const * param_values,
    const size_t * param_value_lens,
    size_t param_count);

/**
 * Appends one chunk of raw format-encoded data to the insert stream.
 * @brief Pushes data to the streaming INSERT (binary-safe, may block for backpressure)
 * @param stream Insert stream handle from chdb_stream_insert()
 * @param data Chunk bytes in the stream's input format; copied, the caller retains ownership
 * @param len Length of the chunk in bytes
 * @return CHDBSuccess on success, CHDBError if the stream already failed or was finalized
 */
CHDB_EXPORT chdb_state chdb_stream_append(chdb_insert_stream stream, const void * data, size_t len);

/**
 * Finalizes the streaming INSERT and commits the data.
 * @brief Signals end-of-input and returns write statistics
 * @param stream Insert stream handle from chdb_stream_insert()
 * @return Result with rows/bytes written, or an error; free with chdb_destroy_query_result()
 * @note Does not free the stream handle; call chdb_destroy_insert_stream() as well
 */
CHDB_EXPORT chdb_result * chdb_stream_done(chdb_insert_stream stream);

/**
 * Cancels ongoing streaming INSERT.
 * @brief Aborts the insert stream without committing (ClickHouse-default semantics, no rollback)
 * @param stream Insert stream handle to cancel
 * @note The handle is freed by chdb_destroy_insert_stream(), not here
 */
CHDB_EXPORT void chdb_stream_cancel_insert(chdb_insert_stream stream);

/**
 * Retrieves error message from the insert stream.
 * @param stream The insert stream handle
 * @return Null-terminated error description, NULL if no error
 */
CHDB_EXPORT const char * chdb_stream_insert_error(chdb_insert_stream stream);

/**
 * Destroys an insert stream handle and releases all associated resources.
 * @param stream The insert stream handle to destroy
 * @note Required for every handle, even on error paths; cancels first if not finalized
 */
CHDB_EXPORT void chdb_destroy_insert_stream(chdb_insert_stream stream);

/**
 * Gets pointer to the result data buffer
 * @param result The query result handle
 * @return Read-only pointer to the result data
 */
CHDB_EXPORT char * chdb_result_buffer(chdb_result * result);

/**
 * Gets the length of the result data
 * @param result The query result handle
 * @return Size of result data in bytes
 */
CHDB_EXPORT size_t chdb_result_length(chdb_result * result);

/**
 * Gets query execution time
 * @param result The query result handle
 * @return Elapsed time in seconds
 */
CHDB_EXPORT double chdb_result_elapsed(chdb_result * result);

/**
 * Gets total rows in query result
 * @param result The query result handle
 * @return Number of rows contained in the result set
 */
CHDB_EXPORT uint64_t chdb_result_rows_read(chdb_result * result);

/**
 * Gets the total bytes occupied by the result set in internal binary format
 * @param result The query result handle
 * @return Number of bytes occupied by the result set in internal binary representation
 */
CHDB_EXPORT uint64_t chdb_result_bytes_read(chdb_result * result);

/**
 * Gets rows read from storage engine
 * @param result The query result handle
 * @return Number of rows read from storage
 */
CHDB_EXPORT uint64_t chdb_result_storage_rows_read(chdb_result * result);

/**
 * Gets bytes read from storage engine
 * @param result The query result handle
 * @return Number of bytes read from storage engine
 */
CHDB_EXPORT uint64_t chdb_result_storage_bytes_read(chdb_result * result);

/**
 * Gets rows written by the query (INSERT write progress)
 * Note: includes rows written by cascaded materialized views, same semantics
 * as X-ClickHouse-Summary.written_rows on the HTTP interface — not just the
 * input rows accepted by the target table.
 * @param result The query result handle
 * @return Number of rows written, 0 for read-only queries
 */
CHDB_EXPORT uint64_t chdb_result_rows_written(chdb_result * result);

/**
 * Gets bytes written by the query (INSERT write progress)
 * Note: includes bytes written by cascaded materialized views, same semantics
 * as X-ClickHouse-Summary.written_bytes on the HTTP interface.
 * @param result The query result handle
 * @return Number of bytes written, 0 for read-only queries
 */
CHDB_EXPORT uint64_t chdb_result_bytes_written(chdb_result * result);

/**
 * Retrieves error message from query execution
 * @param result The query result handle
 * @return Null-terminated error description, NULL if no error
 */
CHDB_EXPORT const char * chdb_result_error(chdb_result * result);

//===--------------------------------------------------------------------===//
// Arrow Integration
//===--------------------------------------------------------------------===//

/**
 * Options controlling Arrow C Data Interface output (chdb_query_arrow family).
 * Pass NULL to any chdb_query_arrow / chdb_stream_query_arrow variant for
 * the default contract (unsupported_as_binary=0,
 * low_cardinality_as_dictionary=0, string_as_string=1).
 *
 * Type mapping note: DateTime columns export as Arrow uint32 (Unix
 * seconds), matching ClickHouse's format="ArrowStream" behavior — the
 * timezone is not carried on the Arrow type. Callers that want a
 * timezone-tagged Arrow timestamp should request DateTime64 in SQL,
 * e.g. `SELECT toDateTime64(col, 0, 'UTC')`, which the kernel maps to
 * arrow::timestamp(SECOND, 'UTC').
 *
 * - unsupported_as_binary: 0 (default) throws UNKNOWN_TYPE for types with
 *   no faithful Arrow mapping (JSON/Object, Dynamic, AggregateFunction).
 *   1 silently degrades them to arrow::binary() like the engine default.
 * - low_cardinality_as_dictionary: 0 (default) materializes LowCardinality
 *   columns to their base type T. 1 emits an Arrow dictionary array
 *   (requires consumer to handle cross-batch dictionary stability).
 * - string_as_string: 1 (default) emits Arrow utf8 for String columns.
 *   0 emits Arrow binary.
 */
typedef struct chdb_arrow_options
{
    int unsupported_as_binary;
    int low_cardinality_as_dictionary;
    int string_as_string;
} chdb_arrow_options;

/**
 * Executes a query and exports the entire result as an Arrow C Data
 * Interface stream into the caller-allocated out_stream. Zero-copy where
 * possible (no IPC serialization, no lz4 round-trip).
 *
 * Ownership: out_stream is filled and ownership of its release() callback
 * transfers to the caller. The underlying ClickHouse buffers are kept
 * alive by the stream's private_data and are released atomically when the
 * caller invokes out_stream->release(out_stream).
 *
 * @param conn Active connection
 * @param query Null-terminated SQL query
 * @param out_stream Caller-allocated ArrowArrayStream (struct from arrow/c/abi.h)
 * @param options Knobs controlling type mapping; pass NULL for defaults
 * @return chdb_result with elapsed/rows_read/bytes_read/storage_* metrics or
 *         an error (buffer/length are NULL/0 — the data is in out_stream)
 */
CHDB_EXPORT chdb_result * chdb_query_arrow(
    chdb_connection conn, const char * query,
    chdb_arrow_stream out_stream, const chdb_arrow_options * options);

/**
 * Binary-safe variant of chdb_query_arrow with explicit query length.
 */
CHDB_EXPORT chdb_result * chdb_query_arrow_n(
    chdb_connection conn, const char * query, size_t query_len,
    chdb_arrow_stream out_stream, const chdb_arrow_options * options);

/**
 * Initializes a streaming Arrow query and returns a stream handle. Each
 * subsequent chdb_stream_fetch_arrow() call pulls one record batch with a
 * stable schema across the lifetime of the stream. Cancel/destroy via the
 * shared chdb_stream_cancel_query / chdb_destroy_query_result functions.
 */
CHDB_EXPORT chdb_result * chdb_stream_query_arrow(
    chdb_connection conn, const char * query,
    const chdb_arrow_options * options);

/**
 * Binary-safe variant of chdb_stream_query_arrow with explicit query length.
 */
CHDB_EXPORT chdb_result * chdb_stream_query_arrow_n(
    chdb_connection conn, const char * query, size_t query_len,
    const chdb_arrow_options * options);

/**
 * chdb_stream_query_arrow() with server-side {name:Type} parameter binding
 * (chdb_query_with_params semantics, no SQL interpolation). Parameters are
 * bound before streaming init and cleared once it returns; fetch, cancel
 * and destroy work exactly like chdb_stream_query_arrow().
 */
CHDB_EXPORT chdb_result * chdb_stream_query_arrow_with_params(
    chdb_connection conn, const char * query,
    const chdb_arrow_options * options,
    const char * const * param_names,
    const char * const * param_values,
    size_t param_count);

/**
 * Binary-safe variant of chdb_stream_query_arrow_with_params.
 */
CHDB_EXPORT chdb_result * chdb_stream_query_arrow_with_params_n(
    chdb_connection conn, const char * query, size_t query_len,
    const chdb_arrow_options * options,
    const char * const * param_names,
    const size_t * param_name_lens,
    const char * const * param_values,
    const size_t * param_value_lens,
    size_t param_count);

/**
 * Pulls the next record batch from a streaming Arrow query into the
 * caller-allocated out_batch (a single-batch ArrowArrayStream). When the
 * stream is exhausted out_batch->get_next() will return a released array
 * on first call. Schema and (if enabled) LowCardinality dictionaries stay
 * stable across all fetches because the converter and cached dictionary
 * values are persisted on the stream handle.
 *
 * @param conn Active connection
 * @param stream_result Stream handle from chdb_stream_query_arrow{,_n}
 * @param out_batch Caller-allocated ArrowArrayStream filled with one batch
 * @return CHDBSuccess on success, CHDBError on protocol error
 */
CHDB_EXPORT chdb_state chdb_stream_fetch_arrow(
    chdb_connection conn, chdb_result * stream_result,
    chdb_arrow_stream out_batch);

/**
 * Registers an Arrow stream as an arrow stream table function with the given name
 * @param conn The connection on which to execute the registration
 * @param table_name Name to register for the arrow stream table function
 * @param arrow_stream chdb Arrow stream handle
 * @return CHDBSuccess on success, CHDBError on failure
 */
CHDB_EXPORT chdb_state chdb_arrow_scan(
    chdb_connection conn, const char * table_name,
    chdb_arrow_stream arrow_stream);

/**
 * Registers an Arrow array as an arrow stream table function with the given name
 * @param conn The connection on which to execute the registration
 * @param table_name Name to register for the arrow stream table function
 * @param arrow_schema chdb Arrow schema handle
 * @param arrow_array chdb Arrow array handle
 * @return CHDBSuccess on success, CHDBError on failure
 */
CHDB_EXPORT chdb_state chdb_arrow_array_scan(
    chdb_connection conn, const char * table_name,
    chdb_arrow_schema arrow_schema, chdb_arrow_array arrow_array);

/**
 * Unregisters an arrow stream table function that was previously registered via chdb_arrow_scan
 * @param conn The connection on which to execute the unregister operation
 * @param table_name Name of the arrow stream table function to unregister
 * @return CHDBSuccess on success, CHDBError on failure
 */
CHDB_EXPORT chdb_state chdb_arrow_unregister_table(chdb_connection conn, const char * table_name);

//===--------------------------------------------------------------------===//
// Backup, Restore and Statement Classification
//===--------------------------------------------------------------------===//

/**
 * Backs a database up into a single archive file.
 *
 * The database name and the destination path are separate arguments and chDB
 * quotes each one for the position it goes in, so a caller never builds
 * `BACKUP ...` text and a name holding a backtick, a quote or a path holding
 * an apostrophe cannot change what runs.
 *
 * The destination is subject to the `backups.allowed_path` configuration
 * parameter, exactly as a hand-written `BACKUP ... TO File(...)` is; a
 * connection that never set it cannot write a backup anywhere. Set it when
 * connecting, e.g. `--backups.allowed_path=/var/lib/chdb/backups`.
 *
 * file_path must be absolute and its directory must already exist. Both are
 * checked here rather than left to the engine: `backups.allowed_path`
 * resolves a relative value against the data directory and a relative
 * file_path then resolves against that, so a relative path lands somewhere no
 * caller intended, and the engine reports a missing directory by naming the
 * allow-list rather than the directory.
 *
 * An existing destination is never overwritten -- the call fails instead. Give
 * every backup its own name and delete the ones you no longer want.
 *
 * Passing base_file_path makes the backup incremental against that archive:
 * only what changed since it is written, and the new archive records the base
 * it needs. Note that the recorded reference is the base's path as given here,
 * so a caller that moves its archives between machines or into object storage
 * has to keep that path reachable, or stay with full backups. NULL, or a
 * length of zero, means a full backup.
 *
 * @param conn Connection whose engine performs the backup
 * @param database Database name, unquoted (e.g. `my-db`, not `` `my-db` ``)
 * @param database_len Length of database in bytes
 * @param file_path Destination archive path, unquoted and absolute
 * @param file_path_len Length of file_path in bytes
 * @param base_file_path Existing archive to make this backup incremental
 *                       against, unquoted and absolute; NULL for a full backup
 * @param base_file_path_len Length of base_file_path in bytes; 0 for a full backup
 * @return Query result; check chdb_result_error() and destroy it with
 *         chdb_destroy_query_result() as for any other result
 */
CHDB_EXPORT chdb_result * chdb_backup_database_n(
    chdb_connection conn,
    const char * database,
    size_t database_len,
    const char * file_path,
    size_t file_path_len,
    const char * base_file_path,
    size_t base_file_path_len);

/**
 * Restores a database from an archive written by chdb_backup_database_n().
 *
 * Quoting, the absolute-path requirement, the `backups.allowed_path`
 * constraint and the result lifecycle match chdb_backup_database_n(); the
 * archive itself must exist. The current database of the session is left
 * alone: restoring into `mem` does not make `mem` current, so a caller that
 * wants to query the restored database has to say so.
 *
 * An incremental archive names its base internally, so there is no base
 * argument here -- but that name is the path the backup was written against,
 * and the restore fails if nothing is there.
 *
 * RESTORE appends to an existing table rather than replacing it, so restore
 * into a database that does not already hold the tables in the archive.
 *
 * @param conn Connection whose engine performs the restore
 * @param database Database name, unquoted
 * @param database_len Length of database in bytes
 * @param file_path Source archive path, unquoted
 * @param file_path_len Length of file_path in bytes
 * @return Query result; check chdb_result_error() and destroy it with
 *         chdb_destroy_query_result()
 */
CHDB_EXPORT chdb_result * chdb_restore_database_n(
    chdb_connection conn,
    const char * database,
    size_t database_len,
    const char * file_path,
    size_t file_path_len);

/**
 * Says what a statement would do, without running it.
 *
 * Parses the SQL with the connection's own parser and settings -- the same
 * parser that would execute it -- and reports what it found. Nothing is
 * executed and the session is left untouched: no current database change, no
 * settings change, no query log entry.
 *
 * The report answers the questions a caller has to settle before it decides
 * whether to run a statement and whether to record it:
 *
 *   statement_count  How many executable statements the text holds. Zero for
 *                    empty input or text that did not parse. A batch is more
 *                    than one, and so is `a PARALLEL WITH b`, because both
 *                    arms execute. A caller that logs statements one at a
 *                    time needs this: only a count of one is a statement it
 *                    can replay on its own.
 *   query_class      What the statement does to state that outlives it; see
 *                    chdb_query_class. A batch takes the maximum over its
 *                    members.
 *   flags            See chdb_query_analysis_flag.
 *
 * target_database names the database the caller considers its own, and is
 * what CHDB_QUERY_WRITES_ONLY_TARGET_DATABASE is judged against. Pass NULL to
 * skip that judgement, in which case the flag is never set.
 *
 * SQL that does not parse reports CHDB_QUERY_UNKNOWN with no flags and a count
 * of zero, and still returns CHDBSuccess -- what it is, is the answer, not an
 * error.
 *
 * @param conn Connection supplying the parser dialect, parser settings, and
 *             the current database used to resolve unqualified names
 * @param sql SQL text to analyse (may contain null bytes)
 * @param sql_len Length of sql in bytes
 * @param target_database The caller's own database, unquoted; may be NULL
 * @param target_database_len Length of target_database in bytes
 * @param out_analysis Receives the report. Set out_analysis->struct_size to
 *                     sizeof(chdb_query_analysis_v1) before the call; every
 *                     field that fits is written on success
 * @return CHDBSuccess, or CHDBError if conn or out_analysis is null, the
 *         connection is closed, or struct_size is smaller than this engine's
 *         minimum
 */
CHDB_EXPORT chdb_state chdb_classify_query_n(
    chdb_connection conn,
    const char * sql,
    size_t sql_len,
    const char * target_database,
    size_t target_database_len,
    chdb_query_analysis_v1 * out_analysis);

//===--------------------------------------------------------------------===//
// Signal Handler Control
//===--------------------------------------------------------------------===//

/**
 * Controls whether chDB installs process-wide signal handlers.
 * Call BEFORE chdb_connect() or query_stable() to prevent chDB
 * from installing process-wide signal handlers.
 *
 * While disabled, no chDB entry point changes the disposition of any signal,
 * so a host that handles deadly signals itself -- a JVM recovering from SIGSEGV
 * as a NullPointerException, for instance -- keeps its handlers across every
 * call. Switching to 0 also takes back the handlers chDB installed while it was
 * enabled, as chdb_reset_signal_handlers() does.
 *
 * The flag is process-wide and sticky; one call per process is enough.
 *
 * @param enabled 1 to enable signal handlers (default), 0 to disable them
 */
CHDB_EXPORT void chdb_set_signal_handlers_enabled(int enabled);

/**
 * Resets the signal handlers installed by chDB back to SIG_DFL.
 * Useful when signal handlers were already installed and need to be removed,
 * e.g. to let the embedding process manage its own signal handling.
 *
 * Only signals chDB installed a handler for are touched; a handler owned by the
 * embedding process is left in place. When chDB installed nothing, this is a
 * no-op.
 */
CHDB_EXPORT void chdb_reset_signal_handlers(void);

//===--------------------------------------------------------------------===//
// Engine Shutdown
//===--------------------------------------------------------------------===//

/**
 * Stops the engine: joins every thread chDB started, so that no chDB thread is
 * alive once this returns. Call it before the host runs a teardown sequence of
 * its own — global destructors, a finalizing language runtime, a sanitizer exit
 * handler — which would otherwise race the still-running engine threads.
 *
 * Close every connection and destroy every result first. While a connection is
 * still open this does nothing and returns CHDBError, because tearing the engine
 * down under a live connection would leave it dangling.
 *
 * Once it starts, the library is closed for business for the rest of the process,
 * whether or not it manages to stop every thread: chdb_connect() then fails and
 * query_stable() must not be called. Calling this again is harmless -- it returns
 * CHDBSuccess if the engine is already stopped, and otherwise retries.
 *
 * Safe to call from any thread and concurrently with itself and with
 * chdb_connect(): callers are serialized, and a connect racing a shutdown either
 * gets in first and is counted, or arrives later and is refused.
 *
 * Skipping it stays as safe as it has always been for a process that just exits:
 * the threads left running are reaped by process exit.
 *
 * @return CHDBSuccess once no chDB thread is left running, CHDBError if a
 *         connection is still open or some thread could not be stopped
 */
CHDB_EXPORT chdb_state chdb_shutdown(void);

#ifdef __cplusplus
}
#endif
