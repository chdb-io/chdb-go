// Package embedded registers the libchdb build for the platform being compiled,
// so a program needs nothing installed on the machine it runs on:
//
//	import _ "github.com/chdb-io/chdb-go/lib/embedded"
//
// That is the whole API. One blank import, with no platform in the path and no
// engine version in your go.mod — this module's go.mod names the four
// lib/<platform> modules and which version of each, and the build takes the one
// that matches. Import a lib/<platform> module directly instead if you want to
// pick the engine version yourself.
//
// CHDB_LIB_PATH still wins when it is set, so a build carrying an engine can be
// pointed at a different one without rebuilding. Otherwise the engine is extracted
// to a cache directory on first run and reused after that.
//
// On a platform none of the four modules covers, nothing is registered and the
// build still succeeds; the engine is then looked up on the machine as usual.
package embedded
