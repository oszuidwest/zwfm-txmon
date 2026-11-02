package i2c

import (
	"testing"
	"unsafe"
)

// The kernel rejects GPIO and I2C ioctls whose struct sizes differ from its own.
func TestKernelStructSizes(t *testing.T) {
	ptr := unsafe.Sizeof(uintptr(0))
	for name, c := range map[string][2]uintptr{
		"gpiochip_info":        {unsafe.Sizeof(chipInfo{}), 68},
		"gpio_v2_line_config":  {unsafe.Sizeof(lineConfig{}), 272},
		"gpio_v2_line_request": {unsafe.Sizeof(lineRequest{}), 592},
		"gpio_v2_line_values":  {unsafe.Sizeof(lineValues{}), 16},
		"i2c_msg":              {unsafe.Sizeof(msg{}), 8 + ptr},
		"i2c_rdwr_ioctl_data":  {unsafe.Sizeof(rdwr{}), 2 * ptr},
	} {
		if c[0] != c[1] {
			t.Errorf("%s: %d bytes, kernel expects %d", name, c[0], c[1])
		}
	}
}
