package services

import (
	"context"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/fleet"
	"github.com/bubbleptr/vpsgogo/packages/core/monitoring"
	"github.com/bubbleptr/vpsgogo/packages/core/notify"
	"github.com/bubbleptr/vpsgogo/packages/core/provisioning"
	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
)

// Backend is everything the views ask of the app. Every method may be slow
// (SSH, the keychain, SQLite): views call them off the UI thread. Tests give
// the views a fake.
type Backend interface {
	// Servers lists the fleet, oldest first.
	Servers(ctx context.Context) ([]fleet.Server, error)
	// Probe connects to host:port and returns its host key fingerprint, for
	// the user to confirm before anything else is sent.
	Probe(ctx context.Context, host string, port int) (fingerprint string, err error)
	// AddServer stores a server whose fingerprint the user confirmed.
	AddServer(ctx context.Context, n fleet.NewServer, fingerprint string) (fleet.Server, error)
	// Provision hardens server id and installs gogo-agent (docs/SPEC.md
	// §4.1), calling progress after each step from the calling goroutine.
	// It is idempotent: after a failure, call it again with the same id.
	Provision(ctx context.Context, id, rootPassword string, progress func(provisioning.StepResult)) error
	// RemoveServer forgets the server, its key and its plan. It leaves the
	// server itself as it is.
	RemoveServer(ctx context.Context, id string) error

	// History returns the agent's samples taken after since.
	History(ctx context.Context, id string, since time.Time) ([]monitoring.Sample, error)
	// Stream calls f with each new sample until ctx ends or the connection
	// drops; it returns nil when ctx ended.
	Stream(ctx context.Context, id string, f func(monitoring.Sample)) error

	// Plans lists the renewal plans of all servers.
	Plans(ctx context.Context) ([]renewal.Subscription, error)
	// SavePlan stores s and, once the server is provisioned, syncs it to the
	// server's agent.
	SavePlan(ctx context.Context, s renewal.Subscription) error
	// DeletePlan removes server id's plan, and syncs that to its agent.
	DeletePlan(ctx context.Context, id string) error
	// Renew records that one more cycle of server id's plan was paid for,
	// syncs it, and returns the plan with its new expiry.
	Renew(ctx context.Context, id string) (renewal.Subscription, error)

	// Settings returns the user's settings.
	Settings(ctx context.Context) (Settings, error)
	// SaveSettings stores s and syncs it to every provisioned server.
	SaveSettings(ctx context.Context, s Settings) error
	// TestChannel sends a test message through one channel.
	TestChannel(ctx context.Context, ch notify.ChannelConfig) error
}

// Settings are the user's, shared by all servers. Channels hold tokens: the
// live backend keeps them in the keychain, never in SQLite.
type Settings struct {
	// Timezone is the user's, such as "Asia/Shanghai": renewal dates and
	// reminders follow the user's calendar. Empty means the system's.
	Timezone string
	// RemindHour is when reminders go out, 0–23.
	RemindHour int
	Channels   []notify.ChannelConfig
	// AnthropicKey is the Claude API key for skills (BYOK); empty when unset.
	AnthropicKey string
}
