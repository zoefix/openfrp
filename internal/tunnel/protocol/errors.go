package protocol

import "errors"

var (
	// ErrUnknownType is returned when a frame carries a message type this
	// build does not recognise.
	ErrUnknownType = errors.New("protocol: unknown message type")

	// ErrMessageTooLarge guards the decoder against a hostile or corrupt
	// length prefix causing a huge allocation.
	ErrMessageTooLarge = errors.New("protocol: message exceeds maximum size")

	// ErrVersionMismatch is returned when the peer speaks an incompatible
	// protocol version.
	ErrVersionMismatch = errors.New("protocol: incompatible version")

	// ErrAuthFailed covers both a bad token and a stale timestamp. The two are
	// deliberately indistinguishable to the peer so the error cannot be used
	// to probe which half was wrong.
	ErrAuthFailed = errors.New("protocol: authentication failed")

	// ErrUnexpectedMessage is returned when a valid message arrives at a point
	// in the exchange where it makes no sense.
	ErrUnexpectedMessage = errors.New("protocol: unexpected message")
)
