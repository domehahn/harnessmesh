package store

// BackendDescriptor makes deployment capabilities explicit for admin tooling
// and future PostgreSQL/object-store implementations. SQLite remains the
// default local backend and satisfies the existing Store contract.
type BackendDescriptor struct {
	Name           string `json:"name"`
	Transactional  bool   `json:"transactional"`
	Distributed    bool   `json:"distributed"`
	ObjectArchive  bool   `json:"object_archive"`
	SupportsReplay bool   `json:"supports_replay"`
}

func SQLiteBackendDescriptor() BackendDescriptor {
	return BackendDescriptor{Name: "sqlite", Transactional: true, Distributed: false, ObjectArchive: false, SupportsReplay: true}
}

// BackendFactory is the seam used by deployments to inject PostgreSQL,
// replicated SQLite, or an object-backed archive without changing domain code.
type BackendFactory interface {
	Open(path string) (Store, error)
	Descriptor() BackendDescriptor
}

type SQLiteBackendFactory struct{}

func (SQLiteBackendFactory) Open(path string) (Store, error) { return OpenSQLite(path) }
func (SQLiteBackendFactory) Descriptor() BackendDescriptor   { return SQLiteBackendDescriptor() }
