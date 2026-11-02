package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oszuidwest/zwfm-txmon/internal/alarm"
	"github.com/oszuidwest/zwfm-txmon/internal/notify"
)

// Config holds the settings from the JSON config file.
type Config struct {
	// Name is shown in the web interface.
	Name string `json:"name"`
	// Port is the HTTP port of the web interface.
	Port int `json:"port"`
	// Username and Password protect the web interface.
	Username string `json:"username"`
	Password string `json:"password"`
	// Address is the I2C address of the TEX (1..31); 0 scans for it.
	Address int `json:"address"`
	// SDA and SCL are the GPIO lines of the bus; Chip is the GPIO chip,
	// empty for the Raspberry Pi header.
	SDA  int    `json:"sda"`
	SCL  int    `json:"scl"`
	Chip string `json:"chip"`
	// Bus selects the kernel I2C adapter /dev/i2c-N instead of GPIO; -1 uses GPIO.
	Bus int `json:"bus"`
	// Alarms and Notifications are edited in the web interface.
	Alarms        alarm.Config  `json:"alarms"`
	Notifications notify.Config `json:"notifications"`
}

func defaultConfig() Config {
	return Config{Name: "TEX", Port: 8080, Username: "admin", SDA: 2, SCL: 3, Bus: -1, Alarms: alarm.Defaults()}
}

// loadConfig reads path over the defaults. An empty path gives the defaults.
func loadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	if path == "" {
		return cfg, nil
	}
	b, err := os.ReadFile(path) //nolint:gosec // the operator chooses the config file
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	switch {
	case c.Address < 0 || c.Address > 31:
		return fmt.Errorf("address %d outside 1..31 (0 scans)", c.Address)
	case c.Port < 1 || c.Port > 65535:
		return fmt.Errorf("port %d is not a TCP port", c.Port)
	}
	return nil
}

// addr is the I2C address; validate keeps it in 0..31.
func (c *Config) addr() uint16 { return uint16(c.Address) } //nolint:gosec // range checked by validate

// validateWeb checks the settings the web interface needs.
func (c *Config) validateWeb() error {
	if c.Username == "" || c.Password == "" {
		return errors.New("the web interface needs a username and password in the config file")
	}
	return nil
}

// saveConfig writes cfg to path. A temporary file and a rename keep the old
// file intact if anything fails.
func saveConfig(path string, cfg *Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ") //nolint:gosec // the config file holds the password by design and is written 0600
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
