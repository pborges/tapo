package tapo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"
)

const defaultDiscoveryTimeout = 3 * time.Second

// DiscoveredDevice is a Kasa/Tapo device found by UDP discovery.
type DiscoveredDevice struct {
	Host    string     `json:"host"`
	Port    int        `json:"port"`
	Device  DeviceInfo `json:"device"`
	Outlets []Outlet   `json:"outlets,omitempty"`
}

// Discover sends a legacy Kasa discovery request and collects replies until
// timeout. With no subnet it uses the limited IPv4 broadcast address. When a
// subnet is supplied, every usable address in that subnet is probed. A bare
// IPv4 address such as "192.168.5.0" means its /24 subnet; CIDR notation is
// also accepted. At most one subnet may be supplied.
//
// A non-positive timeout uses three seconds. The returned slice is sorted by
// IP address and contains no duplicates.
func Discover(ctx context.Context, timeout time.Duration, subnet ...string) ([]DiscoveredDevice, error) {
	if len(subnet) > 1 {
		return nil, errors.New("tapo: discovery accepts at most one subnet")
	}
	destinations := []*net.UDPAddr{{IP: net.IPv4bcast, Port: DefaultPort}}
	if len(subnet) == 1 && strings.TrimSpace(subnet[0]) != "" {
		var err error
		destinations, err = subnetDestinations(subnet[0], DefaultPort)
		if err != nil {
			return nil, err
		}
	}
	return discover(ctx, timeout, destinations...)
}

func discover(ctx context.Context, timeout time.Duration, destinations ...*net.UDPAddr) ([]DiscoveredDevice, error) {
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
	payload := encrypt(request)
	for _, destination := range destinations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, err := conn.WriteToUDP(payload, destination); err != nil {
			return nil, fmt.Errorf("tapo: send discovery to %s: %w", destination, err)
		}
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
	sort.Slice(result, func(i, j int) bool {
		left, leftErr := netip.ParseAddr(result[i].Host)
		right, rightErr := netip.ParseAddr(result[j].Host)
		if leftErr == nil && rightErr == nil {
			return left.Less(right)
		}
		return result[i].Host < result[j].Host
	})
	return result, nil
}

func subnetDestinations(subnet string, port int) ([]*net.UDPAddr, error) {
	value := strings.TrimSpace(subnet)
	if value == "" {
		return nil, errors.New("tapo: discovery subnet is required")
	}
	if !strings.Contains(value, "/") {
		address, err := netip.ParseAddr(value)
		if err != nil || !address.Is4() {
			return nil, fmt.Errorf("tapo: invalid IPv4 discovery subnet %q", subnet)
		}
		value = address.String() + "/24"
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil || !prefix.Addr().Is4() {
		return nil, fmt.Errorf("tapo: invalid IPv4 discovery subnet %q", subnet)
	}
	prefix = prefix.Masked()
	if prefix.Bits() < 16 {
		return nil, fmt.Errorf("tapo: discovery subnet %q is too large (maximum /16)", subnet)
	}

	first := prefix.Addr()
	last := lastAddress(prefix)
	destinations := make([]*net.UDPAddr, 0, 1<<(32-prefix.Bits()))
	for address := first.Next(); address.IsValid() && address.Less(last); address = address.Next() {
		destinations = append(destinations, &net.UDPAddr{IP: net.IP(address.AsSlice()), Port: port})
	}
	// /31 and /32 networks do not have conventional network/broadcast
	// addresses, so probe all addresses in those prefixes.
	if prefix.Bits() >= 31 {
		destinations = destinations[:0]
		for address := first; address.IsValid() && (address == last || address.Less(last)); address = address.Next() {
			destinations = append(destinations, &net.UDPAddr{IP: net.IP(address.AsSlice()), Port: port})
			if address == last {
				break
			}
		}
	}
	return destinations, nil
}

func lastAddress(prefix netip.Prefix) netip.Addr {
	bytes := prefix.Addr().As4()
	hostBits := 32 - prefix.Bits()
	for bit := 0; bit < hostBits; bit++ {
		byteIndex := 3 - bit/8
		bytes[byteIndex] |= 1 << (bit % 8)
	}
	return netip.AddrFrom4(bytes)
}
