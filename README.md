# txmon

Monitor and control R.V.R. Elettronica TEX-LCD FM exciters from a Raspberry Pi. txmon talks to the I2C telemetry port of the TEX, sends alarms when something goes wrong and serves a small web interface.

The protocol and timing have not been checked against a capture of a live bus yet.

## What it does

- Reads forward and reflected power, frequency, temperature, PA voltage and current, efficiency, modulation and deviation every second.
- Shows the alarms of the TEX: PLL lock, power set OK, foldback, interlock, audio loss, SWR and front panel access.
- Sets target power, frequency and RF on or off from the web interface.
- Raises alarms and sends them to a webhook, Microsoft 365 email or Zabbix.
- Detects the firmware of the TEX and uses the right target power encoding for it.
- Finds the I2C address of the TEX when you do not set one.
- Exposes `/metrics` for Prometheus and `/health` for monitoring.

## Requirements

- A Raspberry Pi (any model) with Raspberry Pi OS
- A TEX-LCD exciter with an I2C telemetry port
- A bidirectional I2C level shifter when the TEX uses 5 V on its I2C lines, such as a PCA9306 or a BSS138 module. A TXS0102 does not work for I2C.

## Wiring

txmon drives the bus itself on two GPIO lines. By default these are GPIO 2 (SDA, pin 3) and GPIO 3 (SCL, pin 5), which have 1.8 kΩ pull-ups to 3.3 V on the Pi. Connect SDA, SCL and ground to the I2C port of the TEX, through the level shifter if needed.

The hardware I2C driver must not hold these pins. Make sure `/boot/firmware/config.txt` has `dtparam=i2c_arm=off` or no `dtparam=i2c_arm` line at all.

Other GPIO lines work too: set `sda` and `scl` in the config. Add pull-ups of about 4.7 kΩ to 3.3 V on those lines.

## Installation

There is no installer yet. To set txmon up by hand on the Pi:

1. Download the `linux-arm64` binary (64-bit Raspberry Pi OS) or the `linux-armv6` binary (32-bit) from the [latest release](https://github.com/oszuidwest/zwfm-txmon/releases/latest) and install it as `/usr/local/bin/txmon` with mode 0755.
2. Create a service user that may use the GPIO lines. With `bus` set, it also needs the `i2c` group.

   ```bash
   sudo useradd --system --no-create-home --shell /usr/sbin/nologin txmon
   sudo usermod -aG gpio txmon
   ```

3. Write `/etc/txmon/config.json` as shown below. txmon saves alarm and notification settings to it, so give the directory and the file to `txmon`, with modes 0700 and 0600.
4. Copy `deploy/txmon.service` to `/etc/systemd/system/` and start it with `sudo systemctl enable --now txmon`.

## Configuration

The service reads `/etc/txmon/config.json`:

```json
{
  "name": "TEX Rucphen",
  "port": 8080,
  "username": "admin",
  "password": "secret",
  "address": 0,
  "sda": 2,
  "scl": 3,
  "chip": "",
  "bus": -1
}
```

`address` is the I2C address set on the TEX (1 to 31). With 0 txmon searches for it at start-up and keeps the address it finds. `chip` selects a GPIO chip; empty picks the Pi header. With `bus` set to a number, txmon uses the kernel I2C adapter `/dev/i2c-N` instead of its own GPIO driver. That route lacks the long pauses of the GPIO driver, so only use it when you know your TEX copes.

Alarm and notification settings are also stored in this file, under `alarms` and `notifications`. Edit them in the web interface, which checks the alarm settings and writes the file.

## Command line

```bash
txmon -config /etc/txmon/config.json serve   # web interface
txmon scan                                    # addresses 1..31 that answer as a TEX
txmon read                                    # one reading as JSON
txmon watch                                   # a reading every second
txmon power 80                                # target power in percent
txmon rf off                                  # RF output on or off
txmon freq 98.5                               # frequency in MHz, steps of 0.01
txmon -version
```

Flags such as `-addr`, `-sda`, `-scl` and `-port` override the config file. Only one process can use the GPIO lines, so stop the service before you run commands by hand, or use the web interface.

For development without a transmitter, `-demo` simulates a TEX:

```bash
echo '{"username":"admin","password":"demo"}' > dev.json
go run . -config dev.json -demo serve   # http://localhost:8080
```

## Bus timing

txmon does not use I2C hardware by default. It drives the lines in software, slowly and patiently: about 60 kHz, a 100 µs pause after every good byte, 5.5 ms with both lines low before every STOP, and up to 50 ms of waiting when the TEX stretches the clock. The delays are minimums, so a busy Pi makes phases longer, never shorter.

A few details keep the bus safe:

- When the TEX holds the clock during STOP, txmon waits up to 50 ms for it before it releases SDA, and reports a timeout if the clock stays low.
- When a bit fails while reading, txmon reports an error instead of accepting a broken byte.
- Before it changes power, frequency or RF, txmon reads the transmitter again, and afterwards it reads up to five times to see the new value.
- On TEX firmware below 201, txmon refuses target power above 100 %, because the old encoding cannot express more.

## Alarms

The Settings view in the web interface has a card for each alarm.

| Alarm | Watches |
|---|---|
| Forward Power | forward power below and/or above a limit in W |
| Reflected Power | reflected power above a limit in W |
| Temperature | temperature below and/or above a limit in °C |
| Modulation | total modulation below and/or above a limit in kHz |
| PLL Lock Loss | the PLL is out of lock |
| Audio Loss | the TEX reports audio loss |
| SWR Alarm | the TEX reports an SWR alarm |
| FoldBack | the TEX folds back its power |
| Panel Access | someone uses the front panel |
| Link Loss | polls of the TEX fail |

All alarms are off by default. A value alarm goes off when the value stays past a limit longer than the trigger time, and clears when it stays back on the right side of that same limit longer than the release time. Both times are 3 to 60 seconds, 10 by default. The other alarms use 3 seconds. For the first 15 seconds after the link comes up, only Link Loss is checked. Panel Access sends no recovery, and when the link is lost the other alarms reset without one.

Each alarm counts how often it fired, up to 999. Reset counter on its card sets the count back to zero and arms the alarm again.

## Notifications

Webhook, email and Zabbix each pick which alarms they send and whether they also send recoveries. Test sends a test message with the values in the form, before you save.

- Webhook: a POST with a JSON body with `event` (`alarm` or `recovery`), `transmitter`, `alarm`, `label`, `state`, `value`, `message` and `timestamp`.
- Microsoft 365 email: sent through Microsoft Graph with an app registration (tenant ID, client ID and client secret) from a shared mailbox to one or more recipients.
- Zabbix: a trapper item on the given server, host and key, with the message as its value.
- Heartbeat: a GET to a push URL every minute while txmon runs, for a push monitor such as Uptime Kuma.

The webhook URL, the client secret and the heartbeat URL are never sent back to the browser.

The Events tab lists the alarms and recoveries since txmon started, newest first, up to 100.

## Protocol

A poll is one transaction with a repeated start:

```
S [addr+W] 00 00 00  Sr [addr+R] <64 bytes>  P
```

All words are 16-bit little-endian. Bytes 0 to 15 are the header:

| Word | Meaning |
|---|---|
| h0 | Family ID. TEX and BLUES send 10, PTX sends 1, PJ sends 0x84. |
| h2 | Firmware version of the TEX |
| h3..h7 | Length: `(h3 + h4 + (h5>>4) + (h6>>4) + h7 + 8) * 2` must be 64 |

24 data words follow, word n at byte 16 + 2n:

| Word | Field | Unit |
|---|---|---|
| 8 | target power | % (see below) |
| 9 bit 0 | foldback | |
| 9 bit 1 | interlock | |
| 9 bit 2 | PLL lock, inverted | 0 = locked |
| 10 bit 0 | RF on | |
| 10 bit 2 | power set OK | |
| 10 bit 7 | front panel access | |
| 10 bit 8 | audio loss | |
| 10 bit 15 | SWR alarm | |
| 11-12 | frequency, 32 bits | kHz |
| 15 | modulation | kHz |
| 16, 17 | deviation left, right | kHz |
| 18 | forward power, unsigned | 0.1 W |
| 19 | reflected power | 0.1 W |
| 20 | temperature | °C |
| 21 | PA voltage | 0.1 V |
| 22 | PA current | 0.1 A |
| 23 | efficiency | % |

txmon does not decode words 0 to 7, 13 and 14 or the other bits. It passes all raw words along in its JSON as `header` and `data`.

A write is one register byte (data word + 8) followed by the value, little-endian:

| Action | Bytes |
|---|---|
| target power | `10 lo hi` |
| RF on/off | `12 lo hi`, all of word 10 with bit 0 changed |
| frequency | `13 b0 b1 b2 b3`, in kHz |

On TEX firmware below 201 the target power is not a percentage but a code from 55 to 203. The table is in `internal/tex/tex.go`. txmon picks the encoding from h2.

## License

MIT, see [LICENSE.md](LICENSE.md).
