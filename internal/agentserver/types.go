package agentserver

// This file holds the types the app-server client asks its caller to supply.
//
// They exist so the client depends on nothing of klein's. It drives an external
// process over a JSON-RPC protocol — a job with no opinion about klein's tool
// managers, event bus, or logger — and every klein type reachable from its
// public surface is a type an outside importer would be forced to adopt. So the
// client declares the narrowest interface it can use and takes an instance;
// klein supplies an adapter. See doc/DESIGN.md.
//
// Every one of these is optional: a nil value means the client does without.

// Logger is where the client reports protocol drift — an item type it cannot
// render, a notification method it does not handle, an interrupt the backend
// refused. Diagnostics only: nothing here is on a path the caller acts on, and
// the client never logs a turn's ordinary progress (that is Observer's job).
//
// One method, because that is all the client has to say. klein's *logger.Logger
// satisfies it as-is.
//
// A nil Logger is silent, which is what the tests use. Beware the typed nil: a
// nil *logger.Logger assigned here is a non-nil interface holding a nil
// pointer, and calling it panics. Convert at the injection site, where the
// concrete type is still visible (see backendLogger).
type Logger interface {
	Warn(msg string, keysAndValues ...any)
}
