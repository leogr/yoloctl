package managedchat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
)

const (
	HumanMount        = "/run/yoloctl/chat-server/human"
	EndpointMount     = "/run/yoloctl/chat/endpoint"
	ControlMount      = "/run/yoloctl/chat-control"
	IdentityMount     = "/run/yoloctl/chat/member"
	ServerConfigMount = "/run/yoloctl/chat-server/config"
	ServerDataMount   = "/run/yoloctl/chat-server/data"
	RoomLabel         = "yoloctl.chat.room"
	BoxLabel          = "yoloctl.chat.box-id"
	MemberLabel       = "yoloctl.chat.member"
	RoleLabel         = "yoloctl.chat.role"
	IncarnationLabel  = "yoloctl.chat.incarnation"
)

// Bind is controller-derived infrastructure, never a manifest mount or a host
// grant. Mount the directory so atomic file and socket replacement stays visible.
type Bind struct {
	Source   string
	Target   string
	ReadOnly bool
}

type Projection struct {
	RoomID   string
	DiskID   string
	BoxID    string
	MemberID string
	Server   bool
	Binds    []Bind
}

// Projection selects only the exact declaration identity. Account names confer
// no membership; copying settings or reusing a name grants no native leaf.
// The caller already verified the native disk and holds the space lock when
// preparing a runtime. Ordinary reads never enable a dormant room.
func (s Store) Projection(box Box) (*Projection, error) {
	r, err := s.Load()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !r.RuntimeEnabled {
		return nil, nil
	}
	p := &Projection{RoomID: r.ID, DiskID: r.DiskID, BoxID: box.ID}
	if box.Name == r.Server.Name {
		if box.ID != r.Server.ID {
			return nil, errors.New("managed chat server declaration differs from its reservation")
		}
		p.Server, p.MemberID = true, r.Human.ID
	} else {
		for _, m := range r.Members {
			if !m.Revoked && m.Name == box.Name && m.BoxID == box.ID {
				p.MemberID = m.ID
				break
			}
		}
		if p.MemberID == "" {
			return nil, nil
		}
	}
	if !r.Prepared {
		return nil, errors.New("managed chat storage is not prepared; run chat ensure first")
	}
	if err := s.Verify(r); err != nil {
		return nil, err
	}
	base, _ := layout.ChatRoomRelative(r.ID)
	member, _ := layout.ChatMemberRelative(r.ID, p.MemberID)
	endpoint, _ := layout.ChatEndpointDir(r.ID)
	identityTarget := IdentityMount
	if p.Server {
		identityTarget = HumanMount
	}
	p.Binds = []Bind{
		{Source: endpoint, Target: EndpointMount, ReadOnly: !p.Server},
		{Source: filepath.Join(s.Disk, member), Target: identityTarget, ReadOnly: true},
	}
	if p.Server {
		control, _ := layout.ChatControlDir(r.ID)
		p.Binds = append(p.Binds,
			Bind{Source: control, Target: ControlMount},
			Bind{Source: filepath.Join(s.Disk, base, "server/config"), Target: ServerConfigMount, ReadOnly: true},
			Bind{Source: filepath.Join(s.Disk, base, "server/data"), Target: ServerDataMount},
		)
	}
	return p, nil
}

// EnableRuntime is an explicit desired-state transition. Projection alone never
// starts a workload, changes an existing container, or claims live enforcement.
func (s Store) EnableRuntime(generation uint64, inventory Inventory) (*Room, error) {
	if err := inventory.Validate(); err != nil {
		return nil, err
	}
	r, err := s.Load()
	if err != nil {
		return nil, err
	}
	if r.Generation != generation || !r.Prepared {
		return nil, errors.New("managed chat preparation or generation changed")
	}
	if err := s.Verify(r); err != nil {
		return nil, err
	}
	if r.RuntimeEnabled {
		return r, nil
	}
	r.RuntimeEnabled = true
	r.Generation++
	if err := s.save(r); err != nil {
		return nil, err
	}
	return r, nil
}

// PrepareEndpoints creates only volatile private leaf directories. Existing
// sockets are deliberately retained; the server must prove ownership/liveness
// before replacing them. root is the verified controller run root, not user input.
func PrepareEndpoints(root string, p *Projection, uid, gid int) error {
	if p == nil {
		return nil
	}
	if err := layout.ValidateIdentity(p.RoomID); err != nil {
		return err
	}
	for _, parent := range []string{"chat", "chat-control"} {
		if err := safepath.PrivateDirectory(root, parent, os.Geteuid(), os.Getegid()); err != nil {
			return err
		}
		if err := safepath.PrivateDirectory(root, filepath.Join(parent, p.RoomID), uid, gid); err != nil {
			return err
		}
	}
	return nil
}

func IsProjectionTarget(target string) bool {
	for _, root := range []string{"/run/yoloctl/chat", "/run/yoloctl/chat-server", ControlMount} {
		if target == root || strings.HasPrefix(target, root+"/") {
			return true
		}
	}
	return false
}
