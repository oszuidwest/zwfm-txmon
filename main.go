// Command txmon reads and controls R.V.R. TEX-LCD exciters over I2C and
// serves a small web interface.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"github.com/oszuidwest/zwfm-txmon/internal/i2c"
	"github.com/oszuidwest/zwfm-txmon/internal/notify"
	"github.com/oszuidwest/zwfm-txmon/internal/tex"
)

const usage = `usage: txmon [-config file] [flags] <command>

By default txmon drives the GPIO lines -sda and -scl itself with slow timing.
With -bus it uses the kernel I2C adapter /dev/i2c-N instead. Flags override
the config file.

commands:
  serve             web interface, /api/status and /metrics on the config port
  scan              list addresses 1..31 that answer as a TEX
  read              print one reading as JSON
  watch             print a reading as JSON every second
  power <0..120>    set target power in percent (0..100 on TEX firmware < 201)
  rf <on|off>       switch the RF output
  freq <MHz>        set the frequency, e.g. 98.5

flags:
`

type closer interface {
	tex.Bus
	Close() error
}

func main() {
	configPath := flag.String("config", "", "JSON config file")
	showVersion := flag.Bool("version", false, "print version information and exit")
	demo := flag.Bool("demo", false, "simulate a TEX instead of using the bus (for development)")
	cfg := defaultConfig()
	flag.IntVar(&cfg.SDA, "sda", cfg.SDA, "GPIO line for SDA")
	flag.IntVar(&cfg.SCL, "scl", cfg.SCL, "GPIO line for SCL")
	flag.StringVar(&cfg.Chip, "chip", cfg.Chip, "GPIO chip; empty picks the Pi header chip")
	flag.IntVar(&cfg.Bus, "bus", cfg.Bus, "use kernel I2C adapter /dev/i2c-N instead of GPIO")
	flag.IntVar(&cfg.Address, "addr", cfg.Address, "I2C address of the TEX; 0 scans 1..31")
	flag.IntVar(&cfg.Port, "port", cfg.Port, "HTTP port for serve")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage); flag.PrintDefaults() }
	flag.Parse()
	log.SetFlags(0)

	if *showVersion {
		fmt.Printf("txmon version=%s commit=%s built=%s\n", Version, Commit, BuildTime)
		return
	}
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	// Flags win over the file: load the file, then apply the flags given.
	if *configPath != "" {
		file, err := loadConfig(*configPath)
		if err != nil {
			log.Fatal(err)
		}
		flags := cfg
		cfg = file
		flag.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "sda":
				cfg.SDA = flags.SDA
			case "scl":
				cfg.SCL = flags.SCL
			case "chip":
				cfg.Chip = flags.Chip
			case "bus":
				cfg.Bus = flags.Bus
			case "addr":
				cfg.Address = flags.Address
			case "port":
				cfg.Port = flags.Port
			}
		})
	}
	if err := cfg.validate(); err != nil {
		log.Fatal(err)
	}

	var bus closer
	var err error
	switch {
	case *demo:
		bus, cfg.Address = newDemoBus(), demoAddr
	case cfg.Bus >= 0:
		bus, err = i2c.Open(cfg.Bus)
	default:
		bus, err = i2c.OpenBitBang(cfg.Chip, cfg.SDA, cfg.SCL)
	}
	if err != nil {
		log.Fatal(err)
	}
	err = run(&cfg, *configPath, bus, flag.Arg(0), flag.Args()[1:])
	_ = bus.Close()
	if err != nil {
		log.Fatal(err)
	}
}

var errUsage = errors.New("usage")

func run(cfg *Config, configPath string, bus tex.Bus, cmd string, args []string) error {
	switch cmd {
	case "serve":
		return serve(cfg, configPath, bus)
	case "scan":
		for _, a := range tex.Scan(bus) {
			fmt.Println(a)
		}
		return nil
	}
	dev := tex.Device{Bus: bus, Addr: cfg.addr()}
	if dev.Addr == 0 {
		var err error
		if dev.Addr, err = findOne(bus); err != nil {
			return err
		}
	}
	if cmd == "watch" && len(args) == 0 {
		watch(dev)
	}
	t, err := command(dev, cmd, args)
	if errors.Is(err, errUsage) {
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		return err
	}
	printJSON(t)
	return nil
}

// command runs one of the commands that print a reading.
func command(dev tex.Device, cmd string, args []string) (tex.Telemetry, error) {
	switch {
	case cmd == "read" && len(args) == 0:
		return dev.Read()
	case cmd == "power" && len(args) == 1:
		pct, err := strconv.Atoi(args[0])
		if err != nil {
			return tex.Telemetry{}, fmt.Errorf("power: %w", err)
		}
		return dev.SetTarget(pct)
	case cmd == "rf" && len(args) == 1 && (args[0] == "on" || args[0] == "off"):
		return dev.SetRF(args[0] == "on")
	case cmd == "freq" && len(args) == 1:
		kHz, ok := parseMHz(args[0])
		if !ok {
			return tex.Telemetry{}, fmt.Errorf("freq: %q is not a frequency in MHz with at most two decimals, e.g. 98.5", args[0])
		}
		return dev.SetFrequency(kHz)
	}
	return tex.Telemetry{}, errUsage
}

var mhzRe = regexp.MustCompile(`^(\d{2,3})(?:\.(\d{1,2}))?$`)

// parseMHz turns "98.5" into 98500 kHz. FreqCmd takes steps of 10 kHz, so it
// accepts at most two decimals.
func parseMHz(s string) (int, bool) {
	m := mhzRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	whole, _ := strconv.Atoi(m[1])
	frac, _ := strconv.Atoi((m[2] + "00")[:2])
	return whole*1000 + frac*10, true
}

func printJSON(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

func watch(dev tex.Device) {
	for {
		t, err := dev.Read()
		if err != nil {
			log.Print(err)
			time.Sleep(retryWait)
			continue
		}
		printJSON(t)
		time.Sleep(pollEvery)
	}
}

func serve(cfg *Config, configPath string, bus tex.Bus) error {
	if err := cfg.validateWeb(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st := &settings{path: configPath, cfg: *cfg}
	mon := newMonitor(bus, cfg.addr(), st)
	go mon.Run(ctx)
	go notify.RunHeartbeat(ctx, func() string { return st.get().Notifications.Heartbeat.URL }, time.Minute)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           newServer(st, mon).routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("txmon started", "version", Version, "port", cfg.Port, "address", cfg.Address)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
