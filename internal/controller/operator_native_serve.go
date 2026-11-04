package controller

import (
	"context"
	"errors"
	"net"

	"github.com/leogr/yoloctl/internal/operator"
)

// newNativeOperatorListener is replaced by tests.
var newNativeOperatorListener = defaultNativeOperatorListener

type nativeServedListener interface {
	operator.NativeListenerOwner
	Start(context.Context, func(context.Context, net.Conn)) error
}

// ServeNativeOperator serves a published native operator registration at the
// old path's safety level (leogr's relaxed decision): the controller creates
// the socket in the registration's directory and ServeConn checks the box's
// live grant and incarnation on every request. The listener is recorded on a
// native claim under its incarnation, so only the box's removal can retire it;
// a claim is never released, so this controller never serves it twice.
func (s *Server) ServeNativeOperator(ctx context.Context, identity operator.RegistrationIdentity) error {
	if s == nil || identity.Mode != operator.RegistrationOpenShellNative || identity.Validate() != nil {
		return errors.New("native operator listener requires a native registration identity")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.CheckNativeOperatorPublication(identity.Box); err != nil {
		return err
	}
	c, err := s.operatorPublications.reserveScanned(identity.Endpoint)
	if err != nil {
		return err
	}
	l, err := newNativeOperatorListener(identity)
	if err != nil {
		return err
	}
	principal := Principal{Box: identity.Box, Incarnation: identity.Incarnation}
	if err := l.Start(ctx, func(c context.Context, conn net.Conn) { s.ServeConn(c, conn, principal) }); err != nil {
		return err
	}
	return c.attachNativeListener(l)
}

// reserveScanned claims a native registration found by the controller's
// scanner. Like every native claim it stays occupied, failed or not.
func (p *operatorPublicationClaims) reserveScanned(endpoint operator.Endpoint) (*operatorPublicationClaim, error) {
	if err := endpoint.Validate(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries[endpoint.Incarnation] != nil {
		return nil, errors.New("native operator incarnation is already claimed")
	}
	if len(p.entries) >= 1024 {
		return nil, errors.New("operator publication claim inventory exceeds bound")
	}
	if p.entries == nil {
		p.entries = make(map[string]*operatorPublicationClaim)
	}
	c := &operatorPublicationClaim{table: p, endpoint: endpoint, scanned: true}
	p.entries[endpoint.Incarnation] = c
	return c, nil
}

// CloseNativeOperators retires every listener this controller's scanner
// started, as the old path closes its listeners when the controller stops:
// pending requests are settled and the sockets stay, so a restarted
// controller serves the same registrations again.
func (s *Server) CloseNativeOperators(ctx context.Context) error {
	t := &s.operatorPublications
	t.mu.Lock()
	var listeners []operator.NativeListenerOwner
	for _, c := range t.entries {
		if c.scanned && c.listener != nil && !c.retiring {
			c.retiring = true
			listeners = append(listeners, c.listener)
		}
	}
	t.mu.Unlock()
	var result error
	for _, l := range listeners {
		if _, err := l.RetireListener(ctx, nil); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}
