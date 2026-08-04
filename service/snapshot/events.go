package snapshot

import (
	"context"
	"fmt"
	"net/http"

	"github.com/NimoTech/NimoOS-LocalStorage/codegen/message_bus"
)

// MessageBus event names the scheduler publishes (handoff §3.5). These use
// the cross-service "nimoos:" namespace (mirrored across the ecosystem, e.g.
// NimoOS's own "nimoos:media:created"/"nimoos:file:operate"), distinct from
// this service's own "local-storage:disk:added"-style udev-derived events
// (common/message.go) — snapshot lifecycle events are meant to be consumed
// like other cross-cutting, core-service-style events, not local-storage's
// device events.
const (
	EventSnapshotCreated = "nimoos:snapshot:created"
	EventSnapshotFailed  = "nimoos:snapshot:failed"
	EventSnapshotPaused  = "nimoos:snapshot:paused"
)

// EventPublisher is the scheduler's seam to the MessageBus (misc.go's
// PublishEventWithResponse call is the production pattern this wraps), kept
// deliberately tiny so tests can assert exactly what was published (name +
// properties) without a real bus (task-B3 brief: "event publishing behind a small
// injectable interface").
type EventPublisher interface {
	Publish(ctx context.Context, name string, properties map[string]string) error
}

// MessageBusPublisher is the production EventPublisher.
type MessageBusPublisher struct {
	// NewClient returns a MessageBus client, called fresh on every Publish —
	// mirroring how every existing publisher in this codebase (misc.go,
	// service/disk.go, service/notify.go) calls MyService.MessageBus() anew
	// each time rather than caching a client built once at service startup:
	// the message bus's runtime address is only reliably resolvable once the
	// bus itself is up and registered, which can be after this service
	// starts, so a client cached at construction time could carry a stale
	// or empty address for the rest of the process's life.
	NewClient func() *message_bus.ClientWithResponses
	// SourceID identifies this service as the event's origin (common.ServiceName).
	SourceID string
}

// NewMessageBusPublisher returns a MessageBusPublisher that resolves a fresh
// client (via newClient) on every Publish call.
func NewMessageBusPublisher(newClient func() *message_bus.ClientWithResponses, sourceID string) *MessageBusPublisher {
	return &MessageBusPublisher{NewClient: newClient, SourceID: sourceID}
}

func (m *MessageBusPublisher) Publish(ctx context.Context, name string, properties map[string]string) error {
	if m.NewClient == nil {
		return fmt.Errorf("snapshot: message bus publisher has no client factory configured")
	}
	client := m.NewClient()
	if client == nil {
		return fmt.Errorf("snapshot: message bus client unavailable")
	}
	resp, err := client.PublishEventWithResponse(ctx, m.SourceID, name, properties)
	if err != nil {
		return fmt.Errorf("publish event %s: %w", name, err)
	}
	if resp != nil && resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("publish event %s: unexpected status %s", name, resp.Status())
	}
	return nil
}
