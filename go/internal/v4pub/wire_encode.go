package v4pub

import (
	"github.com/tylinux/nex/go/internal/protocol"
)

// EncodeFrameJSON encodes one logical frame into physical wire frames through
// the shared protocol encoder, carrying the reservation's authoritative
// deliveryKind, logicalFrameId and ordinal. Each returned byte slice is the wire
// frame JSON that becomes the params of a v4/conversation/frame notification.
func EncodeFrameJSON(frame Frame, deliveryKind DeliveryKind, logicalFrameID string, logicalFrameOrdinal int64) ([][]byte, error) {
	wires, err := protocol.EncodeTopicWireFrames(frame.FrameJSON(), protocol.EncodeOptions{
		DeliveryKind:        protocol.DeliveryKind(deliveryKind),
		Topic:               frame.Topic,
		SubscriptionID:      frame.SubscriptionID,
		LogicalFrameID:      logicalFrameID,
		LogicalFrameOrdinal: logicalFrameOrdinal,
	})
	if err != nil {
		return nil, err
	}
	out := make([][]byte, len(wires))
	for i, w := range wires {
		out[i] = w.JSON
	}
	return out, nil
}

// EncodeReservation encodes a reserved logical frame, exactly as
// encodeReservedTopicFrame does on the TS side.
func EncodeReservation(r *Reservation) ([][]byte, error) {
	return EncodeFrameJSON(r.Frame, r.DeliveryKind, r.LogicalFrameID, r.LogicalFrameOrdinal)
}
