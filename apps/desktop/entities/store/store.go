// Package store holds what the app last loaded from the backend: the
// servers, their plans and the settings. The shell loads it; pages read it
// and apply their own changes to it, always on the UI thread (inside an
// async post), so every page shows the same data.
package store

import (
	"slices"

	"github.com/bubbleptr/vpsgogo/apps/desktop/services"
	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
)

// Store is the loaded data.
type Store struct {
	Loaded   bool
	Servers  []fleet.Server
	Plans    []renewal.Subscription
	Settings services.Settings
}

// Server returns server id.
func (s *Store) Server(id string) (fleet.Server, bool) {
	i := slices.IndexFunc(s.Servers, func(v fleet.Server) bool { return v.ID == id })
	if i < 0 {
		return fleet.Server{}, false
	}
	return s.Servers[i], true
}

// Plan returns server id's plan.
func (s *Store) Plan(id string) (renewal.Subscription, bool) {
	i := slices.IndexFunc(s.Plans, func(v renewal.Subscription) bool { return v.ServerID == id })
	if i < 0 {
		return renewal.Subscription{}, false
	}
	return s.Plans[i], true
}

// PutServer adds srv, or replaces the server with its ID.
func (s *Store) PutServer(srv fleet.Server) {
	if i := slices.IndexFunc(s.Servers, func(v fleet.Server) bool { return v.ID == srv.ID }); i >= 0 {
		s.Servers[i] = srv
		return
	}
	s.Servers = append(s.Servers, srv)
}

// RemoveServer forgets server id and its plan.
func (s *Store) RemoveServer(id string) {
	s.Servers = slices.DeleteFunc(s.Servers, func(v fleet.Server) bool { return v.ID == id })
	s.RemovePlan(id)
}

// PutPlan adds p, or replaces its server's plan.
func (s *Store) PutPlan(p renewal.Subscription) {
	if i := slices.IndexFunc(s.Plans, func(v renewal.Subscription) bool { return v.ServerID == p.ServerID }); i >= 0 {
		s.Plans[i] = p
		return
	}
	s.Plans = append(s.Plans, p)
}

// RemovePlan forgets server id's plan.
func (s *Store) RemovePlan(id string) {
	s.Plans = slices.DeleteFunc(s.Plans, func(v renewal.Subscription) bool { return v.ServerID == id })
}

// Name returns server id's name, or id when it is gone.
func (s *Store) Name(id string) string {
	if srv, ok := s.Server(id); ok {
		return srv.Name
	}
	return id
}
