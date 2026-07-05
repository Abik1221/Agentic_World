package mafia

import "errors"

var (
	ErrIllegalAction = errors.New("illegal action for current phase")
	ErrFinished      = errors.New("match finished")
	ErrNotAlive      = errors.New("seat is eliminated")
	ErrUnknownSeat   = errors.New("unknown seat")
)
