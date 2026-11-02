package i2c

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

// Linux GPIO character device, uAPI v2. Just enough for two open-drain lines.
const (
	gpioGetChipInfo = 0x8044b401
	gpioGetLine     = 0xc250b407
	gpioGetValues   = 0xc010b40e
	gpioSetValues   = 0xc010b40f

	lineOutput    = 1 << 3
	lineOpenDrain = 1 << 6
	attrValues    = 2
)

type chipInfo struct {
	name, label [32]byte
	lines       uint32
}

type lineAttr struct {
	id, _ uint32
	val   uint64
}

type lineConfig struct {
	flags    uint64
	numAttrs uint32
	_        [5]uint32
	attrs    [10]struct {
		attr lineAttr
		mask uint64
	}
}

type lineRequest struct {
	offsets      [64]uint32
	consumer     [32]byte
	config       lineConfig
	numLines     uint32
	eventBufSize uint32
	_            [5]uint32
	fd           int32
}

type lineValues struct{ bits, mask uint64 }

// gpioPins drives line 0 (SDA) and 1 (SCL) of one request as open drain.
// Chips without native open drain, like the Pi's, get it emulated by the kernel.
type gpioPins struct {
	f      *os.File
	vals   lineValues
	sticky error
}

// OpenBitBang opens an I2C master on two GPIO lines. An empty chip picks the
// first /dev/gpiochip* whose label contains "pinctrl" (the Pi's header pins).
func OpenBitBang(chip string, sda, scl int) (*BitBang, error) {
	if chip == "" {
		var err error
		if chip, err = findChip(); err != nil {
			return nil, err
		}
	}
	if sda < 0 || scl < 0 || sda == scl {
		return nil, fmt.Errorf("GPIO lines %d and %d: need two different lines", sda, scl)
	}
	c, err := os.Open(chip) //nolint:gosec // the operator chooses the GPIO chip
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	req := lineRequest{numLines: 2}
	req.offsets[0], req.offsets[1] = uint32(sda), uint32(scl) //nolint:gosec // checked non-negative above
	copy(req.consumer[:], "txmon")
	req.config.flags = lineOutput | lineOpenDrain
	req.config.numAttrs = 1
	req.config.attrs[0].attr = lineAttr{id: attrValues, val: 3} // both released
	req.config.attrs[0].mask = 3
	if err := ioctl(c.Fd(), gpioGetLine, unsafe.Pointer(&req)); err != nil {
		if errors.Is(err, syscall.EBUSY) {
			err = fmt.Errorf("%w (another txmon, such as the serve service, or the i2c_arm driver holds them)", err)
		}
		return nil, fmt.Errorf("request GPIO %d and %d on %s: %w", sda, scl, chip, err)
	}
	p := &gpioPins{f: os.NewFile(uintptr(req.fd), "gpio-lines"), vals: lineValues{bits: 3, mask: 3}} //nolint:gosec // fd from the kernel
	return &BitBang{p: p}, nil
}

func findChip() (string, error) {
	chips, _ := filepath.Glob("/dev/gpiochip*")
	for _, path := range chips {
		f, err := os.Open(path) //nolint:gosec // path comes from the /dev/gpiochip* glob
		if err != nil {
			continue
		}
		var info chipInfo
		err = ioctl(f.Fd(), gpioGetChipInfo, unsafe.Pointer(&info))
		_ = f.Close()
		if err == nil && bytes.Contains(info.label[:], []byte("pinctrl")) {
			return path, nil
		}
	}
	return "", fmt.Errorf("no pinctrl GPIO chip in %v; pass -chip", chips)
}

func (p *gpioPins) setSDA(high bool) { p.set(0, high) }
func (p *gpioPins) setSCL(high bool) { p.set(1, high) }

func (p *gpioPins) set(bit uint, high bool) {
	p.vals.bits &^= 1 << bit
	if high {
		p.vals.bits |= 1 << bit
	}
	p.record(ioctl(p.f.Fd(), gpioSetValues, unsafe.Pointer(&p.vals)))
}

func (p *gpioPins) get() (sda, scl bool) {
	v := lineValues{mask: 3}
	p.record(ioctl(p.f.Fd(), gpioGetValues, unsafe.Pointer(&v)))
	return v.bits&1 != 0, v.bits&2 != 0
}

func (p *gpioPins) record(err error) {
	if p.sticky == nil && err != nil {
		p.sticky = fmt.Errorf("gpio: %w", err)
	}
}

func (p *gpioPins) err() error {
	err := p.sticky
	p.sticky = nil
	return err
}

func (p *gpioPins) close() error { return p.f.Close() }

func ioctl(fd, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}
