package managedchat

// Status is a bounded observation, never persisted as applied truth. A ready
// server can coexist with participants waiting for an explicit box recreation.
type Status struct {
	Room           *Room `json:"room"`
	RuntimeApplied bool  `json:"runtime_applied"`
	ServerReady    bool  `json:"server_ready"`
	// SetupRequired is proven absent/stopped/unprepared runtime, not a failed
	// observation. A host opener must not turn ambiguous reads into recovery.
	SetupRequired bool           `json:"setup_required,omitempty"`
	Server        *ServerStatus  `json:"server,omitempty"`
	Members       []MemberStatus `json:"members,omitempty"`
	Pending       []string       `json:"pending,omitempty"`
}

type ServerStatus struct {
	ContainerID          string `json:"container_id"`
	ContainerIncarnation string `json:"container_incarnation"`
	ProcessIncarnation   string `json:"process_incarnation,omitempty"`
	AppliedGeneration    uint64 `json:"applied_generation"`
	ConfigSHA256         string `json:"config_sha256,omitempty"`
	// RecreatePending names deployment drift of the running server, which an
	// explicit recreation applies. The room keeps serving meanwhile, so it
	// is neither pending runtime nor a readiness failure.
	RecreatePending bool   `json:"recreate_pending,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

type MemberStatus struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	BoxID           string `json:"box_id"`
	Revoked         bool   `json:"revoked"`
	Bound           bool   `json:"bound"`
	RecreatePending bool   `json:"recreate_pending"`
	Reason          string `json:"reason,omitempty"`
}

// AttachTarget is observational. Its identities must be revalidated inside
// the ordinary attachment mutation guard; this is not an authority token.
type AttachTarget struct {
	RoomID   string `json:"room_id"`
	MemberID string `json:"member_id"`
	Box      string `json:"box,omitempty"`
	BoxID    string `json:"box_id,omitempty"`
	State    string `json:"state"` // running, stopped, missing, revoked
}
