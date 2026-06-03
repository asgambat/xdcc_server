// Package ircmanager manages persistent IRC connections for the xdcc-server.
// It provides a high-level API to connect/disconnect servers, join/leave channels,
// and emits events for state changes that are propagated via SSE to web clients.
package ircmanager

import "time"

// ---------------------------------------------------------------------------
// Event types for state changes (Fase 3.6)
// ---------------------------------------------------------------------------

// EventType categorizes a state change event emitted by the Manager.
type EventType string

const (
	EventServerConnected     EventType = "server_connected"
	EventServerDisconnected  EventType = "server_disconnected"
	EventServerReconnecting  EventType = "server_reconnecting"
	EventChannelJoined       EventType = "channel_joined"
	EventChannelLeft         EventType = "channel_left"
	EventChannelTopicUpdated EventType = "channel_topic_updated"
)

// Event holds details about a state change in the IRC connection manager.
type Event struct {
	Type       EventType `json:"type"`
	ServerID   int64     `json:"server_id"`
	ServerAddr string    `json:"server_addr"`
	Channel    string    `json:"channel,omitempty"`
	Topic      string    `json:"topic,omitempty"`
	Timestamp  time.Time `json:"timestamp"`
}
