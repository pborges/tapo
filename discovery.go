package tapo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"time"
)

const defaultDiscoveryTimeout = 3 * time.Second

// DiscoveredDevice is a Kasa/Tapo device found by UDP broadcast.
type DiscoveredDevice struct {
	Host    string     `json:"host"`
	Port    int        `json:"port"`
	Device  DeviceInfo `json:"device"`
	Outlets []Outlet   `json:"outlets,omitempty"`
}

// Discover broadcasts a legacy Kasa discovery request and collects replies
// until timeout. A non-positive timeout uses three seconds. The returned slice
// is sorted by IP address and contains no duplicates.
func Discover(ctx context.Context, timeout time.Duration) ([]DiscoveredDevice, error) {
	destination := &net.UDPAddr{IP: net.IPv4bcast, Port: DefaultPort}
	return discover(ctx, timeout, destination)
}

func discover(ctx context.Context, timeout time.Duration, destination *net.UDPAddr) ([]DiscoveredDevice, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = defaultDiscoveryTimeout
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, fmt.Errorf("tapo: start discovery: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	request, err := json.Marshal(map[string]any{"system": map[string]any{"get_sysinfo": nil}})
	if err != nil {
		return nil, err
	}
	if _, err := conn.WriteToUDP(encrypt(request), destination); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("tapo: broadcast discovery: %w", err)
	}
	end := time.Now().Add(timeout)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(end) {
		end = deadline
	}
	if err := conn.SetReadDeadline(end); err != nil {
		return nil, fmt.Errorf("tapo: set discovery deadline: %w", err)
	}

	devices := make(map[string]DiscoveredDevice)
	buf := make([]byte, 65535)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				break
			}
			return nil, fmt.Errorf("tapo: receive discovery response: %w", err)
		}
		var response systemInfoResponse
		if err := json.Unmarshal(decrypt(buf[:n]), &response); err != nil {
			continue
		}
		if response.System.GetSysInfo == nil {
			continue
		}
		wire := *response.System.GetSysInfo
		if wire.ErrorCode != 0 || wire.Model == "" {
			continue
		}
		info, outlets := wire.public()
		devices[from.IP.String()] = DiscoveredDevice{
			Host: from.IP.String(), Port: from.Port, Device: info, Outlets: outlets,
		}
	}

	result := make([]DiscoveredDevice, 0, len(devices))
	for _, device := range devices {
		result = append(result, device)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Host < result[j].Host })
	return result, nil
}
