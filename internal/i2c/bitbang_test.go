package i2c

import (
	"bytes"
	"errors"
	"slices"
	"testing"
	"time"
)

// sim is a wired-AND bus with one simulated slave that echoes what it
// receives into got and transmits out on reads.
type sim struct {
	mSDA, mSCL bool // master drive
	sSDA       bool // slave drive
	addr       byte
	got, out   []byte

	active, first, tx, nack bool
	bit                     int
	cur                     byte
}

func (s *sim) setSDA(h bool)        { s.change(func() { s.mSDA = h }) }
func (s *sim) setSCL(h bool)        { s.change(func() { s.mSCL = h }) }
func (s *sim) get() (sda, scl bool) { return s.mSDA && s.sSDA, s.mSCL }
func (s *sim) err() error           { return nil }
func (s *sim) close() error         { return nil }

func (s *sim) change(f func()) {
	oSDA, oSCL := s.get()
	f()
	sda, scl := s.get()
	switch {
	case oSCL && scl && oSDA && !sda: // (repeated) START
		s.active, s.first, s.tx, s.bit, s.cur, s.sSDA = true, true, false, 0, 0, true
	case oSCL && scl && !oSDA && sda: // STOP
		s.active, s.sSDA = false, true
	case !s.active:
	case !oSCL && scl: // rising edge: sample
		if s.bit < 8 && !s.tx {
			s.cur = s.cur<<1 | b(sda)
		}
		if s.bit == 8 && s.tx {
			s.nack = sda
		}
		s.bit++
	case oSCL && !scl: // falling edge: drive
		switch {
		case s.bit == 8 && s.tx && !s.first:
			s.sSDA = true // master acknowledges
		case s.bit == 8:
			if s.first {
				if s.cur>>1 != s.addr {
					s.active = false
					return
				}
				s.tx = s.cur&1 == 1
			} else {
				s.got = append(s.got, s.cur)
			}
			s.sSDA = false // ACK
		case s.bit == 9:
			s.bit, s.sSDA = 0, true
			if s.tx && (s.first || !s.nack) && len(s.out) > 0 {
				s.cur, s.out = s.out[0], s.out[1:]
				s.sSDA = s.cur&0x80 != 0
			}
			s.first = false
		case s.tx:
			s.sSDA = s.cur>>(7-s.bit)&1 == 1
		}
	}
}

func b(v bool) byte {
	if v {
		return 1
	}
	return 0
}

func TestBitBang(t *testing.T) {
	orig := sleep
	pauses := 0
	sleep = func(d time.Duration) {
		if d == tByte {
			pauses++
		}
	}
	t.Cleanup(func() { sleep = orig })
	block := make([]byte, 64)
	for i := range block {
		block[i] = byte(i*37 + 5) //nolint:gosec // wraps on purpose
	}
	s := &sim{mSDA: true, mSCL: true, sSDA: true, addr: 5, out: block}
	bus := &BitBang{p: s}

	r := make([]byte, 64)
	if err := bus.Tx(5, []byte{0, 0, 0}, r); err != nil {
		t.Fatal(err)
	}
	if !s.nack {
		t.Error("last byte read was not NACKed")
	}
	if pauses != 1+3+1+64 { // after every address and data byte
		t.Errorf("%d pauses of 100 µs in a poll, want 69", pauses)
	}
	pauses = 0
	if err := bus.Tx(5, []byte{0x10, 80, 0}, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r, block) || !bytes.Equal(s.got, []byte{0, 0, 0, 0x10, 80, 0}) {
		t.Errorf("read % x\nslave got % x", r, s.got)
	}
	pauses = 0
	if err := bus.Tx(6, []byte{0}, nil); !errors.Is(err, ErrNack) || pauses != 0 {
		t.Errorf("wrong address: err = %v, %d pauses; want ErrNack and none (straight to STOP)", err, pauses)
	}
	pauses = 0
	if err := bus.Tx(6, nil, r); !errors.Is(err, ErrNack) || pauses != 1 {
		t.Errorf("wrong read address: err = %v, %d pauses; want ErrNack and one", err, pauses)
	}
}

// TestBitBangTiming pins every delay of a write+read transaction. sclHigh
// adds one tShort when the clock is not stretched.
func TestBitBangTiming(t *testing.T) {
	var got []time.Duration
	orig := sleep
	sleep = func(d time.Duration) {
		if d > 0 {
			got = append(got, d)
		}
	}
	t.Cleanup(func() { sleep = orig })
	s := &sim{mSDA: true, mSCL: true, sSDA: true, addr: 5, out: []byte{0xa5}}
	if err := (&BitBang{p: s}).Tx(5, []byte{0x10}, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}

	// The constants themselves, so a change to one shows up here.
	for d, want := range map[time.Duration]time.Duration{tShort: 3480 * time.Nanosecond, tLong: 8480 * time.Nanosecond, tByte: 100 * time.Microsecond, tStop: 5500 * time.Microsecond, tStretch: 50 * time.Millisecond} {
		if d != want {
			t.Errorf("%v, want %v", d, want)
		}
	}

	const us = time.Microsecond
	var want []time.Duration
	add := func(d ...time.Duration) { want = append(want, d...) }
	start := func() { add(15*us, 25*us, tShort, tLong, 75*us) } // 01224F6C
	send := func() {                                            // 01225070, ACK 012251F4, pause 01225378/01225464
		for range 8 {
			add(tShort, tShort, tShort, tShort)
		}
		add(tShort, tShort, tShort, tLong, tShort, tShort, tByte)
	}
	start()
	send() // address, write
	send() // data
	start()
	send() // address, read
	for range 8 {
		add(tLong, tShort, tShort) // 012250F8
	}
	add(tShort, tShort, tShort, tLong, tShort, tByte) // NACK 01225180, pause 0122567A
	add(tStop, tLong)                                 // 012257AC, STOP 01224E6C
	if !slices.Equal(got, want) {
		t.Errorf("delays\n got %v\nwant %v", got, want)
	}
}
