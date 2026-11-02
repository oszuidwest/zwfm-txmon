// Package i2c talks to a Linux i2c-dev adapter using combined transfers,
// so a write and the following read are joined by a repeated start.
package i2c

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

const (
	ioctlRdwr = 0x0707 // I2C_RDWR
	flagRead  = 0x0001 // I2C_M_RD
)

// msg and rdwr mirror struct i2c_msg and struct i2c_rdwr_ioctl_data.
type msg struct {
	addr  uint16
	flags uint16
	len   uint16
	buf   *byte
}

type rdwr struct {
	msgs  *msg
	nmsgs uint32
}

// Bus is a kernel I2C adapter. For clock stretching it relies on the
// adapter; i2c-gpio waits 100 ms by default, longer than BitBang's 50 ms.
type Bus struct {
	f    *os.File
	last time.Time
}

// Open opens /dev/i2c-<n>.
func Open(n int) (*Bus, error) {
	f, err := os.OpenFile(fmt.Sprintf("/dev/i2c-%d", n), os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &Bus{f: f}, nil
}

func (b *Bus) Close() error { return b.f.Close() }

// Tx writes w and then reads len(r) bytes after a repeated start. Either may
// be empty. Transactions are spaced 50 ms apart, as with BitBang.
func (b *Bus) Tx(addr uint16, w, r []byte) error {
	if addr > 0x7f {
		return fmt.Errorf("i2c address %d is not 7-bit", addr)
	}
	if len(w) > 0xffff || len(r) > 0xffff {
		return fmt.Errorf("i2c message longer than %d bytes", 0xffff)
	}
	time.Sleep(time.Until(b.last.Add(tGap)))
	defer func() { b.last = time.Now() }()
	var ms []msg
	if len(w) > 0 {
		ms = append(ms, msg{addr: addr, len: uint16(len(w)), buf: &w[0]}) //nolint:gosec // length checked above
	}
	if len(r) > 0 {
		ms = append(ms, msg{addr: addr, flags: flagRead, len: uint16(len(r)), buf: &r[0]}) //nolint:gosec // length checked above
	}
	if len(ms) == 0 {
		return nil
	}
	d := rdwr{msgs: &ms[0], nmsgs: uint32(len(ms))} //nolint:gosec // at most two messages
	n, _, e := syscall.Syscall(syscall.SYS_IOCTL, b.f.Fd(), ioctlRdwr, uintptr(unsafe.Pointer(&d)))
	runtime.KeepAlive(ms)
	if e != 0 {
		return e
	}
	if int(n) != len(ms) {
		return fmt.Errorf("adapter completed %d of %d messages", n, len(ms))
	}
	return nil
}
