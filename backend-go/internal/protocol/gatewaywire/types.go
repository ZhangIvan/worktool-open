// Package gatewaywire validates the bounded P0 wire1.0 contract. It performs no
// authentication, freshness, state transition, sequence allocation, I/O or retry.
// Canonicalization is restricted to these typed ASCII-key objects, not full JCS.
package gatewaywire

import (
	"errors"
	"time"
)

var (
	ErrInvalid            = errors.New("invalid gateway wire input")
	ErrUnsupportedVersion = errors.New("unsupported gateway wire version")
)

const (
	MaxRawFrameBytes       = 65536
	MaxCanonicalFrameBytes = 8192 // inbound device envelope only
	MaxResultBytes         = 4096
	MaxCommandRefBytes     = 512
)

type Hello struct{ Capabilities []string }
type HelloAck struct {
	TenantID, DeviceID, SessionID string
	PolicyVersion                 int64
	ServerTime                    time.Time
}
type Header struct {
	Version, Kind, EventID, TenantID, DeviceID, SessionID, Mode string
	Seq                                                         int64
	SentAt                                                      time.Time
}
type CommandRef struct{ CommandID, AttemptID string }
type ExecutionResult struct {
	CommandID, AttemptID, Status, EffectState, Code string
	EvidenceRefs                                    []string
}
type TransportAck struct{ AckEventID string }
type ResultAck struct{ AckEventID, CommandID string }
type Heartbeat struct{ PolicyVersion, LastReceivedSeq int64 }
type DeviceEvent struct{ Name, ReferenceID string }
type DeviceBody struct {
	Accepted, Started *CommandRef
	Result            *ExecutionResult
	TransportAck      *TransportAck
	Heartbeat         *Heartbeat
	DeviceEvent       *DeviceEvent
}
type DeviceFrame struct {
	Header          Header
	Body            DeviceBody
	Canonical       []byte
	FrameDigest     string
	ResultCanonical []byte
	ResultDigest    string
}
type TextPayload struct{ TargetID, Text string }
type TextCommandBody struct {
	CommandID, AttemptID, PayloadDigest string
	ExpiresAt                           time.Time
	PolicyVersion                       int64
	RequiresLocalConfirmation           bool
	Payload                             TextPayload
}
type StopBody struct {
	CommandID, AttemptID, Reason string
	PolicyVersion                int64
}
type ConfigBody struct {
	PolicyVersion int64
	Revoked       bool
}
type ServerBody struct {
	Command      *TextCommandBody
	TransportAck *TransportAck
	ResultAck    *ResultAck
	Stop         *StopBody
	Config       *ConfigBody
}
type ServerFrame struct {
	Header Header
	Body   ServerBody
}
