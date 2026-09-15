package game

import (
	"bytes"
	"fmt"
	"io"

	"arkade-poker/go/internal/covenant"
	"arkade-poker/go/internal/ports"
)

// Events are private persistence records. Diagnostics never print their payload.
func (e Event) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "<private game event>") }

func EncodeEvent(e Event) ([]byte, error) {
	mask := 0
	if e.Config != nil {
		mask |= 1
	}
	if e.Invitation != nil {
		mask |= 2
	}
	if e.Secrets != nil {
		mask |= 4
	}
	if e.Message != nil {
		mask |= 8
	}
	if e.Spend != nil {
		mask |= 16
	}
	if e.Accepted != nil {
		mask |= 32
	}
	if e.PrivateHoles != nil {
		mask |= 64
	}
	if e.Evaluation != nil {
		mask |= 128
	}
	if e.Receipt != nil {
		mask |= 256
	}
	if e.Publication != nil {
		mask |= 512
	}
	expected := 0
	switch e.Kind {
	case PublicationPrepared:
		expected = 512
	case Configured:
		if e.Config != nil { // Preserve canonical legacy records on replay.
			expected = 1
		}
	case SessionPrepared:
		expected = 2 | 4 | 8
	case MessagePrepared, MessagePublished, MessageReceived:
		expected = 8
	case SpendPrepared, SpendSigned:
		expected = 16
	case DepositObserved, SpendObserved:
		expected = 32
	case OpeningObserved:
		expected = 32 | 64
	case ShowdownEvaluated:
		expected = 128
	case SubmissionAttempted:
		expected = 256
	case SessionOpened, SetupAborted, SubmissionFailed:
	default:
		return nil, ErrEncoding
	}
	if mask != expected || e.Kind != SessionPrepared && e.Role != 0 || e.Kind != SetupAborted && e.Kind != SubmissionFailed && e.FailureCode != "" || e.Kind != ShowdownEvaluated && e.Winner != 0 {
		return nil, ErrEncoding
	}
	if e.Kind == SessionPrepared && e.Role != covenant.Player1 && e.Role != covenant.Player2 {
		return nil, ErrEncoding
	}
	// Times affect only deadline admission; every other setup event carries zero.
	if e.ObservedAt != 0 && !(e.Kind == MessageReceived && e.Message != nil && e.Message.Kind == FinalShuffle || e.Kind == DepositObserved || e.Kind == SpendObserved || e.Kind == OpeningObserved || e.Kind == SubmissionAttempted) {
		return nil, ErrEncoding
	}
	w := new(encoder)
	w.header(eventDomain)
	w.raw(e.SessionID[:])
	w.u64(e.Sequence)
	w.raw(e.Previous[:])
	w.u64(uint64(e.ObservedAt))
	w.u8(byte(e.Kind))
	switch e.Kind {
	case PublicationPrepared:
		if len(e.Publication.Payload) == 0 || len(e.Publication.Carrier) == 0 {
			return nil, ErrEncoding
		}
		w.blob(e.Publication.Payload, MaxMessageBytes)
		w.blob(e.Publication.Carrier, MaxCarrierBytes)
	case Configured:
		var b []byte
		if e.Config != nil {
			var err error
			b, err = encodeConfig(*e.Config)
			if err != nil {
				return nil, err
			}
		}
		w.blob(b, MaxEventBytes)
	case SessionPrepared:
		b, err := invitationBinary(*e.Invitation)
		if err != nil {
			return nil, err
		}
		id, err := invitationID(*e.Invitation)
		if err != nil || id != e.Invitation.SessionID {
			return nil, ErrInvitation
		}
		w.blob(b, 4096)
		w.u8(byte(e.Role))
		secrets, err := e.Secrets.MarshalBinary()
		if err != nil {
			return nil, err
		}
		defer clear(secrets)
		w.raw(secrets)
		message, err := EncodeMessage(*e.Message)
		if err != nil {
			return nil, err
		}
		w.blob(message, MaxMessageBytes)
	case MessagePrepared, MessagePublished, MessageReceived:
		b, err := EncodeMessage(*e.Message)
		if err != nil {
			return nil, err
		}
		w.blob(b, MaxMessageBytes)
	case SpendPrepared, SpendSigned:
		encodeSaved(w, *e.Spend)
	case DepositObserved, SpendObserved:
		encodeAccepted(w, *e.Accepted)
	case OpeningObserved:
		encodeAccepted(w, *e.Accepted)
		for _, v := range *e.PrivateHoles {
			encodeCardReveal(w, v)
		}
	case ShowdownEvaluated:
		if e.Winner > covenant.Player2 {
			return nil, ErrEncoding
		}
		encodeShowdown(w, *e.Evaluation)
		w.u8(byte(e.Winner))
	case SubmissionAttempted:
		encodeReceipt(w, *e.Receipt)
	case SubmissionFailed:
		if e.FailureCode != "submission_failed" && e.FailureCode != "retry_failed" {
			return nil, ErrEncoding
		}
		w.str(e.FailureCode, 64)
	case SetupAborted:
		if !validAbortReason(e.FailureCode) {
			return nil, ErrEncoding
		}
		w.str(e.FailureCode, 64)
	}
	w.checksum()
	b, err := w.finish()
	if len(b) > MaxEventBytes {
		return nil, ErrEncoding
	}
	return b, err
}
func DecodeEvent(data []byte) (Event, error) {
	body, err := checkedBody(data, MaxEventBytes)
	if err != nil {
		return Event{}, err
	}
	r := &decoder{data: body}
	r.header(eventDomain)
	var e Event
	copy(e.SessionID[:], r.raw(32))
	e.Sequence = r.u64()
	copy(e.Previous[:], r.raw(32))
	e.ObservedAt = covenant.UnixSeconds(r.u64())
	e.Kind = EventKind(r.u8())
	success := false
	defer func() {
		if !success && e.Secrets != nil {
			_ = e.Secrets.Destroy()
		}
	}()
	switch e.Kind {
	case PublicationPrepared:
		e.Publication = &ports.PreparedMessage{Payload: r.blob(MaxMessageBytes), Carrier: r.blob(MaxCarrierBytes)}
	case Configured:
		if b := r.blob(MaxEventBytes); len(b) != 0 {
			c, err := decodeConfig(b)
			if err != nil {
				return Event{}, err
			}
			e.Config = &c
		}
	case SessionPrepared:
		inv, err := decodeInvitationBinary(r.blob(4096))
		if err != nil {
			return Event{}, err
		}
		e.Invitation = &inv
		e.Role = covenant.Player(r.u8())
		e.Secrets, err = DecodeSessionSecrets(r.raw(131))
		if err != nil {
			return Event{}, err
		}
		m, err := DecodeMessage(r.blob(MaxMessageBytes))
		if err != nil {
			return Event{}, err
		}
		e.Message = &m
	case MessagePrepared, MessagePublished, MessageReceived:
		m, err := DecodeMessage(r.blob(MaxMessageBytes))
		if err != nil {
			return Event{}, err
		}
		e.Message = &m
	case SpendPrepared, SpendSigned:
		v := decodeSaved(r)
		e.Spend = &v
	case DepositObserved, SpendObserved:
		v := decodeAccepted(r)
		e.Accepted = &v
	case OpeningObserved:
		v := decodeAccepted(r)
		e.Accepted = &v
		holes := [2]covenant.CardReveal{decodeCardReveal(r), decodeCardReveal(r)}
		e.PrivateHoles = &holes
	case ShowdownEvaluated:
		v := decodeShowdown(r)
		e.Evaluation = &v
		e.Winner = covenant.Player(r.u8())
	case SubmissionAttempted:
		v := decodeReceipt(r)
		e.Receipt = &v
	case SessionOpened:
	case SetupAborted, SubmissionFailed:
		e.FailureCode = r.str(64)
	default:
		return Event{}, ErrEncoding
	}
	if err := r.done(); err != nil {
		return Event{}, err
	}
	b, err := EncodeEvent(e)
	if err != nil {
		return Event{}, err
	}
	defer clear(b)
	if !bytes.Equal(b, data) {
		return Event{}, ErrEncoding
	}
	success = true
	return e, nil
}
func validAbortReason(reason string) bool {
	return reason == "invalid_peer_evidence" || reason == "initial_deadline_expired" || reason == "funding_unavailable"
}
