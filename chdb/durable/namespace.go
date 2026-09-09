package durable

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"sync"
)

// Namespaces: the entry point to the durable control plane.
//
// A namespace is a URL plus an engine factory. The URL says where objects live
// and, through its scheme, which backend implements the conditional
// operations; the factory says how to bring up an engine for whichever object
// is being opened.
//
// One constraint is not the namespace's to hide: chdb-core binds one data path
// per process, so one process holds one open durable object at a time. Fan-out
// across objects is therefore sequential, or spread across worker processes. A
// registry here that pretended otherwise would only move the failure somewhere
// less obvious (contract §3.6).

// SchemeFactory builds a backend for one object, given the namespace URL and
// the object id.
type SchemeFactory func(ctx context.Context, u *url.URL, objectID string) (Backend, error)

var (
	schemesMu sync.RWMutex
	schemes   = map[string]SchemeFactory{}
)

// RegisterBackendScheme registers a backend for a URL scheme.
//
// The local filesystem and S3-compatible schemes are registered by this
// package. A provider that is not — a bespoke store, a fault-injecting wrapper
// — registers itself here, which is also how it can live in its own module
// without this one depending on it.
func RegisterBackendScheme(scheme string, factory SchemeFactory) {
	schemesMu.Lock()
	defer schemesMu.Unlock()
	schemes[strings.TrimSuffix(scheme, ":")] = factory
}

func lookupScheme(scheme string) (SchemeFactory, bool) {
	schemesMu.RLock()
	defer schemesMu.RUnlock()
	factory, ok := schemes[scheme]
	return factory, ok
}

func registeredSchemes() []string {
	schemesMu.RLock()
	defer schemesMu.RUnlock()
	names := make([]string, 0, len(schemes))
	for name := range schemes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// NamespaceOptions configures a namespace.
type NamespaceOptions struct {
	// EngineFactory says how to build the engine for an object being opened.
	// Defaults to NewChdbEngine(), which runs the engine this process loaded.
	EngineFactory EngineFactory

	// BackendFactory bypasses scheme lookup and supplies the backend
	// directly. Tests use it to wrap a real backend in fault injection.
	BackendFactory BackendFactory

	// Owner is the default writer name for objects opened from this
	// namespace.
	Owner string

	// ScratchRoot is the default parent directory for scratch trees.
	ScratchRoot string

	// Tuning holds the default lease and commit parameters, field by field.
	Tuning Tuning
}

// Namespace addresses durable objects on one backend.
type Namespace struct {
	u       *url.URL
	options NamespaceOptions
}

// NewNamespace binds a namespace to a URL.
//
//	file:///var/lib/chdb-durable
//	s3://bucket/prefix?region=eu-west-1
//
// The scheme decides the backend. An unregistered scheme is refused here
// rather than at the first open, so a typo in a configuration file fails at
// startup.
func NewNamespace(rawURL string, options NamespaceOptions) (*Namespace, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, wrapError(CategoryBackend, err, "durable: %q is not a URL", rawURL)
	}
	if options.EngineFactory == nil {
		options.EngineFactory = NewChdbEngine()
	}
	if options.BackendFactory == nil {
		if _, ok := lookupScheme(u.Scheme); !ok {
			return nil, newError(CategoryBackend,
				"durable: no backend registered for scheme %q; registered: %s",
				u.Scheme, strings.Join(registeredSchemes(), ", "))
		}
	}
	return &Namespace{u: u, options: options}, nil
}

// URL is the namespace's location.
func (n *Namespace) URL() string { return n.u.String() }

// Open opens one object.
//
// It returns the object and whether it already existed — a caller creating a
// tenant on first use needs to tell "restored" from "created" without a
// separate probe. A writer lease is taken unless OpenOptions.ReadOnly is set.
//
// An open that fails leaves nothing behind: any lease it took is released, the
// engine is closed and the scratch directory is removed.
func (n *Namespace) Open(ctx context.Context, objectID string, options OpenOptions) (*Object, bool, error) {
	if err := validateObjectID(objectID); err != nil {
		return nil, false, err
	}

	var (
		backend Backend
		err     error
	)
	if n.options.BackendFactory != nil {
		backend, err = n.options.BackendFactory(ctx, objectID)
	} else {
		factory, ok := lookupScheme(n.u.Scheme)
		if !ok {
			return nil, false, newError(CategoryBackend,
				"durable: no backend registered for scheme %q", n.u.Scheme)
		}
		backend, err = factory(ctx, n.u, objectID)
	}
	if err != nil {
		return nil, false, err
	}

	if options.Owner == "" {
		options.Owner = n.options.Owner
	}
	if options.ScratchRoot == "" {
		options.ScratchRoot = n.options.ScratchRoot
	}
	options.Tuning = mergeTuning(n.options.Tuning, options.Tuning)

	return openObject(ctx, objectID, backend, n.options.EngineFactory, options)
}

// mergeTuning lets a per-open override win field by field over the
// namespace's default, and leaves the rest to take theirs.
func mergeTuning(base, override Tuning) Tuning {
	out := base
	if override.LeaseTTL != 0 {
		out.LeaseTTL = override.LeaseTTL
	}
	if override.HeartbeatInterval != 0 {
		out.HeartbeatInterval = override.HeartbeatInterval
	}
	if override.ClockSkewAllowance != 0 {
		out.ClockSkewAllowance = override.ClockSkewAllowance
	}
	if override.CommitDeadline != 0 {
		out.CommitDeadline = override.CommitDeadline
	}
	if override.MaxCommitAttempts != 0 {
		out.MaxCommitAttempts = override.MaxCommitAttempts
	}
	return out
}
