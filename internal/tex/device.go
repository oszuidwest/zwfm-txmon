package tex

import (
	"errors"
	"fmt"
	"time"
)

// Bus is a combined write-then-read I2C transfer (repeated start).
type Bus interface {
	Tx(addr uint16, w, r []byte) error
}

type Device struct {
	Bus  Bus
	Addr uint16
}

// Read polls the device once: write 00 00 00, read 64 bytes.
func (d Device) Read() (Telemetry, error) {
	b := make([]byte, BlockLen)
	if err := d.Bus.Tx(d.Addr, []byte{0, 0, 0}, b); err != nil {
		return Telemetry{}, fmt.Errorf("i2c address %d: %w", d.Addr, err)
	}
	return Parse(b)
}

// Scan returns the addresses 1..31 that answer with a valid TEX block.
func Scan(bus Bus) []uint16 {
	var found []uint16
	for a := uint16(1); a <= 31; a++ {
		if _, err := (Device{bus, a}).Read(); err == nil {
			found = append(found, a)
		}
	}
	return found
}

// SetTarget sets the target power in percent. It reads the firmware version
// first, so old TEX firmware gets the legacy encoding.
func (d Device) SetTarget(pct int) (Telemetry, error) {
	return d.apply(func(t Telemetry) ([]byte, error) { return TargetCmd(pct, t.Firmware) },
		func(t Telemetry) bool { return t.TargetPct == pct })
}

func (d Device) SetRF(on bool) (Telemetry, error) {
	return d.apply(func(t Telemetry) ([]byte, error) { return RFCmd(t.Data[wStatus2], on), nil },
		func(t Telemetry) bool { return t.OnAir == on })
}

func (d Device) SetFrequency(kHz int) (Telemetry, error) {
	return d.apply(func(Telemetry) ([]byte, error) { return FreqCmd(kHz) },
		func(t Telemetry) bool { return int(t.FrequencyMHz*1000+0.5) == kHz })
}

// apply reads the current state, writes cmd and polls until done reports the
// new value or a few seconds pass.
func (d Device) apply(cmd func(Telemetry) ([]byte, error), done func(Telemetry) bool) (Telemetry, error) {
	t, err := d.Read()
	if err != nil {
		return t, err
	}
	b, err := cmd(t)
	if err != nil {
		return t, err
	}
	if err := d.Bus.Tx(d.Addr, b, nil); err != nil {
		return t, fmt.Errorf("write % x: %w", b, err)
	}
	for range 5 {
		if err != nil {
			time.Sleep(2 * time.Second) // 3 s after a failed poll
		}
		time.Sleep(time.Second)
		if t, err = d.Read(); err == nil && done(t) {
			return t, nil
		}
	}
	if err == nil {
		err = errors.New("the transmitter did not report the new value")
	}
	return t, err
}
