package main

import (
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"sync"
)

const demoAddr = 5

// demoBus answers like a 300 W TEX at address demoAddr, so the web
// interface can be developed without a transmitter.
type demoBus struct {
	mu   sync.Mutex
	data [24]uint16
}

func newDemoBus() *demoBus {
	b := &demoBus{}
	d := &b.data
	d[8] = 80                  // target power %
	d[10] = 1 | 1<<2           // on air, power set OK
	d[11], d[12] = 0x80c4, 0x1 // 98500 kHz
	d[15], d[16], d[17] = 72, 70, 71
	d[20] = 38  // °C
	d[21] = 480 // 48.0 V
	d[23] = 68  // %
	return b
}

func (b *demoBus) Close() error { return nil }

func (b *demoBus) Tx(addr uint16, w, r []byte) error {
	if addr != demoAddr {
		return errors.New("no ACK from slave")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	d := &b.data
	if len(r) == 0 && len(w) >= 3 { // write: register, value
		word := int(w[0]) - 8
		d[word] = binary.LittleEndian.Uint16(w[1:])
		if len(w) == 5 {
			d[word+1] = binary.LittleEndian.Uint16(w[3:])
		}
		return nil
	}
	fwd := 0.0
	if d[10]&1 == 1 {
		fwd = 3000 * float64(d[8]) / 100 * (0.99 + rand.Float64()*0.02) //nolint:gosec // demo jitter, in 0.1 W
	}
	d[18] = uint16(fwd)
	d[19] = uint16(fwd * 0.004)
	d[22] = uint16(fwd / 480 / 0.68 * 10) // 0.1 A from P = U*I*efficiency
	header := []uint16{10, 0, 210, 24, 0, 0, 0, 0}
	for i, v := range append(header, d[:]...) {
		binary.LittleEndian.PutUint16(r[2*i:], v)
	}
	return nil
}
