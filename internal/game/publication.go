package game

import "arkade-poker/go/internal/ports"

// Bounds only: the carrier format and its signature verification belong to the
// transport. It must bind Payload when preparing/authenticating that carrier.
const MaxCarrierBytes = 2 * MaxMessageBytes

func (g *Game) PendingPublication() (ports.PreparedMessage, error) {
	if g == nil || g.setup == nil || g.setup.publication == nil {
		return ports.PreparedMessage{}, ErrInput
	}
	p := g.setup.publication
	return ports.PreparedMessage{Payload: append([]byte(nil), p.Payload...), Carrier: append([]byte(nil), p.Carrier...)}, nil
}
