package chatstream

// HistoryRequest captures the visible channel and original request identity
// before a tab can change. Completion returns that same ID and Channel.
type HistoryRequest struct{ ID, Channel, Before string }

// Action is a bounded human operation, never an arbitrary IRC command. Kind is
// join, part, topic-read, topic-set, topic-clear, away or back. Only topic-set
// supplies Topic; only away supplies Reason; away/back carry no channel.
type Action struct {
	ID, Kind, Channel string
	Topic             *string
	Reason            *string // away only; connection-wide, no channel
	// Catalog is an optional trusted host refresh for an explicit join. The
	// driver validates and applies it before admitting that same action, so
	// independent background catalog delivery cannot race a new-channel join.
	// It is never supplied by the UI or forwarded to the IRC wire.
	Catalog *Catalog
}

// CatalogRead correlates one host refresh locally. It is never wire authority.
type CatalogRead struct{ Link, Sequence uint64 }

// Catalog is supplied only by the host's typed controller read/mutation result.
// It must never be built from an untrusted caller-supplied stream channel list.
type Catalog struct {
	Read   CatalogRead `json:"-"`
	Header Header
	// RuntimeUnobserved distinguishes a desired-only read from observed pending.
	RuntimeUnobserved bool
	RuntimeApplied    bool
	Pending           []string
}
