package tex

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// block builds a valid TEX block; set changes data words before encoding.
func block(firmware uint16, set func(d *[24]uint16)) []byte {
	h := [8]uint16{tranID, 0, firmware, 24} // 24 data words: (24+8)*2 = 64 bytes
	var d [24]uint16
	if set != nil {
		set(&d)
	}
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, h) // cannot fail on a bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, d)
	return b.Bytes()
}

func TestParse(t *testing.T) {
	got, err := Parse(block(210, func(d *[24]uint16) {
		d[wFreq], d[wFreq+1] = 98500&0xffff, 98500>>16
		d[wTarget] = 80
		d[wFwd] = 35000 // 3500.0 W, above int16
		d[wRfl] = 0xfffe
		d[wTemp] = 41
		d[wStatus1] = 1 << 2 // PLL unlocked
		d[wStatus2] = 1 | 1<<15
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got.FrequencyMHz != 98.5 || got.TargetPct != 80 || got.ForwardW != 3500 || got.ReflectedW != -0.2 ||
		got.TemperatureC != 41 || got.PLLLocked || !got.OnAir || !got.SWRAlarm || got.Foldback {
		t.Errorf("Parse = %+v", got)
	}
}

func TestParseRejects(t *testing.T) {
	ptx := block(210, nil)
	ptx[0] = 1
	short := block(210, nil)
	short[6] = 23
	for name, c := range map[string]struct {
		b    []byte
		want string
	}{
		"other family": {ptx, "PTX"},
		"bad length":   {short, "announces 62 bytes"},
	} {
		if _, err := Parse(c.b); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestLegacyTarget(t *testing.T) {
	// Target power codes for old firmware.
	for pct, raw := range map[int]uint16{0: 55, 1: 56, 50: 151, 100: 203} {
		if got := targetRaw(pct, 200); got != raw {
			t.Errorf("%d%% -> %d, want %d", pct, got, raw)
		}
	}
	for pct := 0; pct <= 100; pct++ {
		raw := targetRaw(pct, 200)
		if raw < legacyBase || targetPct(raw, 200) != pct {
			t.Fatalf("pct %d -> raw %d -> %d", pct, raw, targetPct(raw, 200))
		}
	}
	if targetRaw(42, 201) != 42 || targetPct(42, 201) != 42 {
		t.Error("firmware >= 201 must use percent directly")
	}
}

func TestCommands(t *testing.T) {
	if _, err := TargetCmd(120, 201); err != nil {
		t.Error("firmware >= 201 must accept 120%")
	}
	if _, err := TargetCmd(101, 200); err == nil {
		t.Error("legacy firmware must reject 101%")
	}
	tgt, _ := TargetCmd(100, 200)
	freq, _ := FreqCmd(98500)
	rf := RFCmd(0x8004, true)
	for name, c := range map[string]struct{ got, want []byte }{
		"target": {tgt, []byte{16, 203, 0}},
		"freq":   {freq, []byte{19, 0xc4, 0x80, 0x01, 0x00}},
		"rf":     {rf, []byte{18, 0x05, 0x80}},
	} {
		if !bytes.Equal(c.got, c.want) {
			t.Errorf("%s = % x, want % x", name, c.got, c.want)
		}
	}
}
