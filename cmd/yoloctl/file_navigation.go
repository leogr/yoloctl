package main

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/leogr/yoloctl/internal/filelink"
	"github.com/leogr/yoloctl/internal/followview"
	"github.com/leogr/yoloctl/internal/termio"
)

// A single native view may publish participant and file requests concurrently.
var viewTabMu sync.Mutex

type fileNavigationSource struct {
	Path    string
	Bounded bool
}

func fileNavigation(ctx context.Context, c filelink.Context, requests *os.File, source ...fileNavigationSource) (*filelink.Navigator, func(filelink.Target) error, error) {
	if err := c.Validate(); err != nil {
		return nil, nil, err
	}
	n := &filelink.Navigator{Context: c}
	if c.Mode == "host" {
		n.Probe = followview.HostProbe
	} else {
		n.Probe = func(ctx context.Context, targets []filelink.Target) map[filelink.Target]error {
			valid := []filelink.Target{}
			out := map[filelink.Target]error{}
			for _, t := range targets {
				if !c.Accept(t) {
					out[t] = errors.New("file request changed its pinned context")
				} else {
					valid = append(valid, t)
				}
			}
			r := followVMReader(c.Space, c.Base)
			for t, e := range r.Probe(ctx, valid) {
				out[t] = e
			}
			return out
		}
	}
	var registrationErr error
	if requests != nil {
		viewTabMu.Lock()
		registration := termio.NavigationRegistration(c)
		if len(source) > 0 {
			registration.FollowPath = source[0].Path
			registration.FollowBounded = source[0].Bounded
		}
		registrationErr = termio.WriteTabRequest(requests, registration)
		viewTabMu.Unlock()
	}
	open := func(t filelink.Target) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if registrationErr != nil {
			return registrationErr
		}
		if requests == nil {
			return errors.New("open local files from the yoloctl TUI")
		}
		if !c.Accept(t) {
			return errors.New("file request changed its pinned context")
		}
		viewTabMu.Lock()
		defer viewTabMu.Unlock()
		return termio.WriteTabRequest(requests, termio.FollowRequest(t))
	}
	return n, open, nil
}
