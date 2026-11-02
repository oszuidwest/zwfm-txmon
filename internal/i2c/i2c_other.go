//go:build !linux

package i2c

import "errors"

var errOS = errors.New("i2c is only supported on Linux")

type Bus struct{}

func Open(int) (*Bus, error) { return nil, errOS }

func (*Bus) Close() error { return nil }

func (*Bus) Tx(uint16, []byte, []byte) error { return errOS }

func OpenBitBang(string, int, int) (*BitBang, error) { return nil, errOS }
