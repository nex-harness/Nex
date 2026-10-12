package protocol

import "strings"

// Sizes of the three carriers a topic notification travels through, measured the
// same way as measureTopicNotificationEnvelopeBytes in wire-codec.ts. The
// encoder compares MaxBytes with the physical frame limit, so these numbers
// decide where a logical frame is split; any drift changes fragment boundaries.
type EnvelopeSizes struct {
	CLINdjsonBytes     int
	ChannelSocketBytes int
	MobileRelayBytes   int
	MaxBytes           int
}

const (
	notificationMethod = "v4/conversation/frame"
	// {"method":"v4/conversation/frame","params":}\n  -> everything except the wire JSON
	channelEventResponseType = 204 // ResponseType.EventFire in @nex/rpc
	socketProtocolHeader     = 13
)

// vqlByteLength is the VQL length of value>>>0, as in the TypeScript helper.
func vqlByteLength(value int) int {
	n := 1
	for rem := uint32(value) >> 7; rem > 0; rem >>= 7 {
		n++
	}
	return n
}

var (
	cliEnvelopeOverhead = len(`{"method":"`+notificationMethod+`","params":}`) + 1
	maxEventIDJSONBytes = len("9007199254740991")
	maxEventIDSerial    = 1 + vqlByteLength(maxEventIDJSONBytes) + maxEventIDJSONBytes
	channelHeaderBytes  = 1 + vqlByteLength(2) + 1 + vqlByteLength(channelEventResponseType) + maxEventIDSerial
	mobileRelayFixed    = func() int {
		id := strings.Repeat("x", TransportEnvelopeIDMaxChars)
		const big = "9007199254740991"
		return len(`{"type":"data","payload":{"nex_type":"rpc-frame","bridgeSessionId":"` + id +
			`","bridgeGeneration":` + big + `,"recoveryId":"` + id + `","seq":` + big +
			`,"dataBase64":""},"client_ts":` + big + `,"server_ts":` + big + `}`)
	}()
)

// MeasureEnvelope takes the UTF-8 length of the wire frame's JSON.
func MeasureEnvelope(wireJSONBytes int) EnvelopeSizes {
	channelPayload := channelHeaderBytes + 1 + vqlByteLength(wireJSONBytes) + wireJSONBytes
	s := EnvelopeSizes{
		CLINdjsonBytes:     cliEnvelopeOverhead + wireJSONBytes,
		ChannelSocketBytes: channelPayload + socketProtocolHeader,
		MobileRelayBytes:   mobileRelayFixed + 4*((channelPayload+2)/3),
	}
	s.MaxBytes = max(s.CLINdjsonBytes, s.ChannelSocketBytes, s.MobileRelayBytes)
	return s
}
