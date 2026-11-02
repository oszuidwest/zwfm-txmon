package i2c

import (
	"errors"
	"fmt"
	"time"
)

// pins drives two open-drain lines; high releases a line. Errors are sticky
// and reported by err, so the bit logic stays readable.
type pins interface {
	setSDA(high bool)
	setSCL(high bool)
	get() (sda, scl bool)
	err() error
	close() error
}

// Bus timing of the software I2C master.
const (
	tShort   = 3480 * time.Nanosecond  // most bit phases
	tLong    = 8480 * time.Nanosecond  // START hold and read sampling
	tByte    = 100 * time.Microsecond  // after a byte (see send, recv, Tx)
	tStop    = 5500 * time.Microsecond // both lines held low before STOP
	tStretch = 50 * time.Millisecond   // longest clock stretch accepted
	tGap     = 50 * time.Millisecond   // between transactions
)

// sleep waits at least d. Tests replace it.
var sleep = func(d time.Duration) {
	if d > time.Millisecond {
		time.Sleep(d)
		return
	}
	for end := time.Now().Add(d); time.Now().Before(end); {
	}
}

var (
	ErrNack    = errors.New("no ACK from slave")
	errStretch = errors.New("slave held SCL low for more than 50 ms")
	errBusy    = errors.New("bus busy: SDA or SCL is low before START")
)

// BitBang is an I2C master on two GPIO lines with slow timing: about
// 60 kHz, clock stretching, a 100 µs pause after each good byte and a 5.5 ms
// hold before STOP. Every delay is a minimum; scheduling can only make phases longer.
type BitBang struct {
	p    pins
	last time.Time
}

func (b *BitBang) Close() error { return b.p.close() }

func (b *BitBang) Tx(addr uint16, w, r []byte) (err error) {
	if addr > 0x7f {
		return fmt.Errorf("i2c address %d is not 7-bit", addr)
	}
	sleep(time.Until(b.last.Add(tGap)))
	defer func() {
		if e := b.stop(); err == nil {
			err = e
		}
		b.last = time.Now()
		if e := b.p.err(); e != nil {
			err = e
		}
	}()
	if len(w) > 0 {
		if err := b.start(byte(addr << 1)); err != nil { //nolint:gosec // addr <= 0x7f checked above
			return err
		}
		for _, c := range w {
			if err := b.send(c); err != nil {
				return fmt.Errorf("data byte: %w", err)
			}
		}
	}
	if len(r) > 0 {
		if err := b.start(byte(addr<<1 | 1)); err != nil { //nolint:gosec // addr <= 0x7f checked above
			if errors.Is(err, ErrNack) {
				sleep(tByte) // pause after a NACKed read address too
			}
			return err
		}
		for i := range r {
			if r[i], err = b.recv(i < len(r)-1); err != nil {
				return err
			}
		}
	}
	return nil
}

// start sends a (repeated) START and the address byte. It drives the START
// edges even when it finds the bus busy, then reports it.
func (b *BitBang) start(addr byte) error {
	b.p.setSDA(true)
	sleep(15 * time.Microsecond)
	b.p.setSCL(true)
	sleep(25 * time.Microsecond)
	_ = b.sclHigh() // the busy check below covers a timeout
	var err error
	if sda, scl := b.p.get(); !sda || !scl {
		err = errBusy
	}
	b.p.setSDA(false)
	sleep(tLong)
	b.p.setSCL(false)
	sleep(75 * time.Microsecond)
	if err != nil {
		return err
	}
	if err := b.send(addr); err != nil {
		return fmt.Errorf("address %d: %w", addr>>1, err)
	}
	return nil
}

// send writes one byte and reads the slave's ACK. A good byte is followed by
// a 100 µs pause; after an error the transaction goes to STOP without it
// (Tx adds it back for a NACKed read address).
func (b *BitBang) send(c byte) error {
	for i := 7; i >= 0; i-- {
		sleep(tShort)
		b.p.setSDA(c>>i&1 == 1)
		sleep(tShort)
		b.p.setSCL(true)
		if err := b.sclHigh(); err != nil {
			return err
		}
		sleep(tShort)
		b.p.setSCL(false)
	}
	// Ninth clock. Finish it even if the clock wait fails.
	sleep(tShort)
	b.p.setSDA(true)
	sleep(tShort)
	b.p.setSCL(true)
	err := b.sclHigh()
	sleep(tLong)
	nack, _ := b.p.get()
	sleep(tShort)
	b.p.setSCL(false)
	sleep(tShort)
	if err == nil && nack {
		err = ErrNack
	}
	if err == nil {
		sleep(tByte)
	}
	return err
}

// recv reads one byte and answers with ACK, or NACK on the last byte. After a
// failed bit it still sends a NACK, and every byte ends with the 100 µs pause.
// The failure is reported, so a broken byte is never accepted.
func (b *BitBang) recv(ack bool) (c byte, err error) {
	defer sleep(tByte)
	b.p.setSDA(true)
	for range 8 {
		c <<= 1
		sleep(tLong)
		b.p.setSCL(true)
		if err = b.sclHigh(); err != nil {
			ack = false
			break
		}
		if sda, _ := b.p.get(); sda {
			c |= 1
		}
		sleep(tShort)
		b.p.setSCL(false)
	}
	// Ninth clock, finished even if the clock wait fails.
	sleep(tShort)
	b.p.setSDA(!ack)
	sleep(tShort)
	b.p.setSCL(true)
	e := b.sclHigh()
	sleep(tLong)
	b.p.setSCL(false)
	sleep(tShort)
	b.p.setSDA(true)
	if err == nil {
		err = e
	}
	return c, err
}

func (b *BitBang) sclHigh() error {
	sleep(tShort)
	return b.stretch()
}

// stretch waits up to 50 ms while the slave holds SCL low and adds one short
// phase once it lets go.
func (b *BitBang) stretch() error {
	if _, scl := b.p.get(); scl {
		return nil
	}
	defer sleep(tShort)
	for end := time.Now().Add(tStretch); ; {
		if _, scl := b.p.get(); scl {
			return nil
		}
		if time.Now().After(end) {
			return errStretch
		}
	}
}

// stop holds both lines low for 5.5 ms, then releases SCL and, 8.48 µs later,
// SDA. It waits up to 50 ms for a stretched clock before releasing SDA and
// reports a timeout if the clock stays low.
func (b *BitBang) stop() error {
	b.p.setSDA(false)
	b.p.setSCL(false)
	sleep(tStop)
	b.p.setSCL(true)
	sleep(tLong)
	err := b.stretch()
	b.p.setSDA(true)
	return err
}
