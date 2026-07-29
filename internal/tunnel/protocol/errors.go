package protocol

import "errors"

var (
	ErrUnknownType = errors.New("protocol: unknown message type")

	ErrMessageTooLarge = errors.New("protocol: message exceeds maximum size")

	ErrVersionMismatch = errors.New("protocol: incompatible version")

	ErrAuthFailed = errors.New("protocol: authentication failed")

	ErrUnexpectedMessage = errors.New("protocol: unexpected message")
)
