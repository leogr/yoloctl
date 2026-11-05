package agentprofile

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
)

func (p *Profile) validateStateMounts() error {
	if len(p.StateMounts) == 0 {
		return nil
	}
	if p.StateContainerPath == "" {
		return fmt.Errorf("agent-profile %q: state_mounts requires state_container_path", p.Name)
	}
	destinations := []string{path.Clean(p.StateContainerPath)}
	for _, mount := range p.StateMounts {
		if mount.Source == "." || !fs.ValidPath(mount.Source) || strings.ContainsAny(mount.Source, "\\\x00") {
			return fmt.Errorf("agent-profile %q: state_mounts source must be a clean directory below the agent leaf", p.Name)
		}
		dest := mount.Destination
		if !path.IsAbs(dest) || path.Clean(dest) != dest || !strings.HasPrefix(dest, "/home/sandbox/") || strings.ContainsAny(dest, "\\\x00") {
			return fmt.Errorf("agent-profile %q: state_mounts destination must be a clean path below /home/sandbox", p.Name)
		}
		for _, prior := range destinations {
			if dest == prior || strings.HasPrefix(dest, prior+"/") || strings.HasPrefix(prior, dest+"/") {
				return fmt.Errorf("agent-profile %q: overlapping managed state destinations", p.Name)
			}
		}
		destinations = append(destinations, dest)
	}
	return nil
}
