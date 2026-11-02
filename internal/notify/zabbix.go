package notify

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// ZabbixConfig sends the event message as the value of a trapper item.
type ZabbixConfig struct {
	Server string `json:"server"`
	Port   int    `json:"port"`
	Host   string `json:"host"`
	Key    string `json:"key"`
	Events Events `json:"events"`
}

func (c *ZabbixConfig) configured() bool {
	return c.Server != "" && c.Host != "" && c.Key != ""
}

var zabbixMagic = []byte("ZBXD\x01")

func (c *ZabbixConfig) send(ctx context.Context, e *Event) error {
	port := c.Port
	if port == 0 {
		port = 10051
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(c.Server, strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("zabbix: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}

	data, err := json.Marshal(map[string]any{
		"request": "sender data",
		"data":    []map[string]string{{"host": c.Host, "key": c.Key, "value": e.Message()}},
	})
	if err != nil {
		return err
	}
	// Header: "ZBXD\x01" and the little-endian payload length.
	msg := binary.LittleEndian.AppendUint64(bytes.Clone(zabbixMagic), uint64(len(data)))
	if _, err := conn.Write(append(msg, data...)); err != nil {
		return fmt.Errorf("zabbix: %w", err)
	}

	header := make([]byte, 13)
	if _, err := io.ReadFull(conn, header); err != nil || !bytes.Equal(header[:5], zabbixMagic) {
		return fmt.Errorf("zabbix: bad reply header: %w", err)
	}
	n := binary.LittleEndian.Uint64(header[5:])
	if n == 0 || n > 64<<10 {
		return fmt.Errorf("zabbix: reply of %d bytes", n)
	}
	reply := make([]byte, n)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("zabbix: %w", err)
	}
	var resp struct{ Response, Info string }
	if err := json.Unmarshal(reply, &resp); err != nil {
		return fmt.Errorf("zabbix: %w", err)
	}
	// Zabbix answers "success" even when it drops the item; the counters in
	// info show it, e.g. "processed: 0; failed: 1".
	var processed int
	if _, err := fmt.Sscanf(resp.Info, "processed: %d;", &processed); resp.Response != "success" || (err == nil && processed == 0) {
		return fmt.Errorf("zabbix did not accept the item (check host, key, trapper type and allowed hosts): %s", resp.Info)
	}
	return nil
}
