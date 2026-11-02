// Package tex implements the I2C telemetry protocol of R.V.R. TEX-LCD
// exciters.
//
// A poll writes three zero address bytes and reads a 64-byte block after a
// repeated start. The block holds a 16-byte header and 24 little-endian data
// words. A write sends one register byte (data word + 8) and the new value.
package tex

import (
	"encoding/binary"
	"fmt"
)

const (
	BlockLen  = 64
	headerLen = 16
	tranID    = 10 // header word 0 of TEX (and BLUES) blocks

	// Below this firmware the target power register holds a code from legacyTarget.
	legacyFirmware = 201
)

// Data word numbers, counted after the header.
const (
	wTarget  = 8
	wStatus1 = 9
	wStatus2 = 10
	wFreq    = 11 // 32-bit with word 12, in kHz
	wMod     = 15
	wDevL    = 16
	wDevR    = 17
	wFwd     = 18
	wRfl     = 19
	wTemp    = 20
	wPAV     = 21
	wPAI     = 22
	wEff     = 23
)

// families maps header word 0 to the RVR family and that family's block
// length.
var families = map[int]struct {
	name string
	len  int
}{
	1:    {"PTX-LCD/PTX-DDS/PTX-LCDDSP", 0x90},
	10:   {"TEX-LCD or BLUES", 0x40},
	0x84: {"PJ amplifier", 0x60},
}

// legacyTarget converts target codes 55..203 to percent, for firmware < 201.
var legacyTarget = [...]uint8{
	0, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 5, 6, 6, 6, 6, 7, 7, 7,
	8, 8, 9, 9, 9, 10, 10, 11, 11, 11, 11, 12, 12, 13, 13, 14, 14, 15, 15, 15,
	16, 16, 17, 17, 18, 19, 19, 20, 20, 21, 21, 22, 22, 23, 24, 24, 25, 25, 26,
	27, 27, 28, 28, 29, 30, 30, 31, 32, 32, 33, 34, 34, 35, 36, 37, 37, 38, 39,
	39, 40, 41, 42, 43, 43, 44, 45, 46, 47, 47, 48, 49, 50, 51, 52, 52, 53, 54,
	55, 56, 57, 58, 59, 60, 61, 62, 62, 63, 64, 65, 66, 67, 68, 69, 70, 71, 72,
	73, 74, 75, 76, 77, 78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 91,
	92, 93, 94, 95, 96, 97, 98, 99, 100,
}

const legacyBase = 55

// Telemetry holds the 20 decoded fields of a TEX block, with their units and
// scaling. Header and Data keep the raw words.
type Telemetry struct {
	Firmware      int        `json:"firmware"`
	FrequencyMHz  float64    `json:"frequency_mhz"`
	TargetPct     int        `json:"target_power_pct"`
	ForwardW      float64    `json:"forward_power_w"`
	ReflectedW    float64    `json:"reflected_power_w"`
	TemperatureC  int        `json:"temperature_c"`
	ModulationKHz int        `json:"modulation_khz"`
	DeviationLKHz int        `json:"deviation_left_khz"`
	DeviationRKHz int        `json:"deviation_right_khz"`
	PAVoltage     float64    `json:"pa_voltage_v"`
	PACurrent     float64    `json:"pa_current_a"`
	EfficiencyPct int        `json:"efficiency_pct"`
	OnAir         bool       `json:"on_air"`
	PowerSetOK    bool       `json:"power_set_ok"`
	PLLLocked     bool       `json:"pll_locked"`
	Foldback      bool       `json:"foldback"`
	Interlock     bool       `json:"interlock"`
	AudioLoss     bool       `json:"audio_loss"`
	SWRAlarm      bool       `json:"swr_alarm"`
	PanelAccess   bool       `json:"panel_access"`
	Header        [8]uint16  `json:"header"`
	Data          [24]uint16 `json:"data"`
}

// Parse validates a 64-byte block and decodes it.
func Parse(b []byte) (Telemetry, error) {
	var t Telemetry
	if len(b) != BlockLen {
		return t, fmt.Errorf("block is %d bytes, want %d", len(b), BlockLen)
	}
	for i := range t.Header {
		t.Header[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	for i := range t.Data {
		t.Data[i] = binary.LittleEndian.Uint16(b[headerLen+2*i:])
	}
	h := t.Header
	if id := s16(h[0]); id != tranID {
		if f, ok := families[id]; ok {
			return t, fmt.Errorf("device is a %s (id %d, %d-byte block), not a TEX", f.name, id, f.len)
		}
		return t, fmt.Errorf("bad transmitter id %#04x, want %#04x (header % x)", h[0], tranID, b[:headerLen])
	}
	if n := (int(h[3]) + int(h[4]) + int(h[5]>>4) + int(h[6]>>4) + int(h[7]) + 8) * 2; n != BlockLen {
		return t, fmt.Errorf("header announces %d bytes, want %d (header % x)", n, BlockLen, b[:headerLen])
	}

	d := t.Data
	bit := func(w, n int) bool { return d[w]>>n&1 == 1 }
	t.Firmware = int(h[2])
	// Signed, truncated to 10 kHz before scaling.
	t.FrequencyMHz = float64(s32(uint32(d[wFreq])|uint32(d[wFreq+1])<<16)/10) / 100
	t.TargetPct = targetPct(d[wTarget], t.Firmware)
	t.ForwardW = float64(d[wFwd]) / 10 // unsigned: TEX3500 exceeds 3276.7 W
	t.ReflectedW = float64(s16(d[wRfl])) / 10
	t.TemperatureC = s16(d[wTemp])
	t.ModulationKHz = s16(d[wMod])
	t.DeviationLKHz = s16(d[wDevL])
	t.DeviationRKHz = s16(d[wDevR])
	t.PAVoltage = float64(s16(d[wPAV])) / 10
	t.PACurrent = float64(s16(d[wPAI])) / 10
	t.EfficiencyPct = s16(d[wEff])
	t.Foldback = bit(wStatus1, 0)
	t.Interlock = bit(wStatus1, 1)
	t.PLLLocked = !bit(wStatus1, 2) // the bit is set on lock loss
	t.OnAir = bit(wStatus2, 0)
	t.PowerSetOK = bit(wStatus2, 2)
	t.PanelAccess = bit(wStatus2, 7)
	t.AudioLoss = bit(wStatus2, 8)
	t.SWRAlarm = bit(wStatus2, 15)
	return t, nil
}

// s16 and s32 read a word as two's complement.
func s16(v uint16) int { return int(int16(v)) } //nolint:gosec // deliberate sign reinterpretation
func s32(v uint32) int { return int(int32(v)) } //nolint:gosec // deliberate sign reinterpretation

func targetPct(raw uint16, firmware int) int {
	if firmware >= legacyFirmware {
		return s16(raw)
	}
	i := min(s16(raw), legacyBase+len(legacyTarget)-1) - legacyBase
	if i < 0 {
		return 0
	}
	return int(legacyTarget[i])
}

func targetRaw(pct, firmware int) uint16 {
	if firmware >= legacyFirmware {
		return uint16(pct) //nolint:gosec // TargetCmd keeps pct in 0..120
	}
	for i, p := range legacyTarget {
		if int(p) == pct {
			return uint16(legacyBase + i)
		}
	}
	return legacyBase + uint16(len(legacyTarget)) - 1
}

// InputError is a value the transmitter does not accept. Nothing was sent.
type InputError struct{ msg string }

func (e *InputError) Error() string { return e.msg }

// write builds a register write: register byte (data word + 8), then the value.
func write(word int, value uint32, n int) []byte {
	b := []byte{byte(word + 8), 0, 0, 0, 0} //nolint:gosec // word is a constant below 24
	binary.LittleEndian.PutUint32(b[1:], value)
	return b[:1+n]
}

// TargetCmd sets the target power in percent, encoded for the given firmware.
// Allowed is 0..120 %; the legacy table only reaches 100 %.
func TargetCmd(pct, firmware int) ([]byte, error) {
	limit := 120
	if firmware < legacyFirmware {
		limit = 100
	}
	if pct < 0 || pct > limit {
		return nil, &InputError{fmt.Sprintf("target power %d%% out of range 0..%d for TEX firmware %d", pct, limit, firmware)}
	}
	return write(wTarget, uint32(targetRaw(pct, firmware)), 2), nil
}

// RFCmd switches the RF output. It rewrites the whole status word 10,
// keeping its other bits, so pass the word from a recent reading.
func RFCmd(status uint16, on bool) []byte {
	w := status &^ 1
	if on {
		w |= 1
	}
	return write(wStatus2, uint32(w), 2)
}

// FreqCmd sets the frequency in kHz: 87.50..108.10 MHz in 10 kHz steps.
func FreqCmd(kHz int) ([]byte, error) {
	if kHz < 87500 || kHz > 108100 || kHz%10 != 0 {
		return nil, &InputError{fmt.Sprintf("frequency %d kHz outside 87.50..108.10 MHz or not on a 10 kHz step", kHz)}
	}
	return write(wFreq, uint32(kHz), 4), nil
}
