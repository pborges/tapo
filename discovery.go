package tapo

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash/crc32"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"
)

const defaultDiscoveryTimeout = 3 * time.Second

// ModernDiscoveryPort is the UDP port used by current TP-Link discovery.
const ModernDiscoveryPort = 20002

const discoveryInitialCRC = uint32(0x5a6b7c8d)

// DiscoveredDevice is a Kasa/Tapo device found by UDP discovery.
type DiscoveredDevice struct {
	Host     string     `json:"host"`
	Port     int        `json:"port"`
	HTTPPort int        `json:"http_port,omitempty"`
	Device   DeviceInfo `json:"device"`
}

// Discover sends a TP-Link discovery request on UDP port 20002 and collects
// replies until timeout. With no subnet it uses the limited IPv4 broadcast
// address. When a subnet is supplied, every usable address in that subnet is
// probed. A bare IPv4 address such as "192.168.5.0" means its /24 subnet; CIDR
// notation is also accepted. At most one subnet may be supplied.
//
// A non-positive timeout uses three seconds. The returned slice is sorted by
// IP address and contains no duplicates.
func Discover(ctx context.Context, timeout time.Duration, subnet ...string) ([]DiscoveredDevice, error) {
	if len(subnet) > 1 {
		return nil, errors.New("tapo: discovery accepts at most one subnet")
	}
	destinations := []*net.UDPAddr{{IP: net.IPv4bcast, Port: ModernDiscoveryPort}}
	if len(subnet) == 1 && strings.TrimSpace(subnet[0]) != "" {
		var err error
		destinations, err = subnetDestinations(subnet[0], ModernDiscoveryPort)
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
	if err := enableBroadcast(conn); err != nil {
		return nil, fmt.Errorf("tapo: enable discovery broadcast: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	payload, err := modernDiscoveryQuery()
	if err != nil {
		return nil, fmt.Errorf("tapo: build discovery request: %w", err)
	}
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
		response, err := parseModernDiscoveryResponse(buf[:n])
		if err != nil {
			continue
		}
		if response.ErrorCode != 0 || response.Result.DeviceModel == "" {
			continue
		}
		result := response.Result
		httpPort := result.Encryption.HTTPPort
		if httpPort == 0 {
			httpPort = defaultHTTPPort
		}
		info := DeviceInfo{
			Alias: result.DeviceModel, Model: result.DeviceModel, DeviceID: result.DeviceID,
			MAC: result.MAC, HardwareVersion: result.HWVer, SoftwareVersion: result.FWVer,
		}
		devices[from.IP.String()] = DiscoveredDevice{
			Host: from.IP.String(), Port: from.Port, HTTPPort: httpPort, Device: info,
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

func modernDiscoveryQuery() ([]byte, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate RSA key: %w", err)
	}
	publicDER := x509.MarshalPKCS1PublicKey(&privateKey.PublicKey)
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: publicDER})
	request, err := json.Marshal(map[string]any{
		"params": map[string]string{"rsa_key": string(publicPEM)},
	})
	if err != nil {
		return nil, fmt.Errorf("encode RSA key: %w", err)
	}
	if len(request) > int(^uint16(0)) {
		return nil, errors.New("discovery request is too large")
	}

	payload := make([]byte, 16+len(request))
	payload[0] = 2                              // TDP version
	payload[1] = 0                              // message type
	binary.BigEndian.PutUint16(payload[2:4], 1) // probe operation
	binary.BigEndian.PutUint16(payload[4:6], uint16(len(request)))
	payload[6] = 17 // flags
	payload[7] = 0  // padding
	if _, err := rand.Read(payload[8:12]); err != nil {
		return nil, fmt.Errorf("generate device serial: %w", err)
	}
	binary.BigEndian.PutUint32(payload[12:16], discoveryInitialCRC)
	copy(payload[16:], request)
	binary.BigEndian.PutUint32(payload[12:16], crc32.ChecksumIEEE(payload))
	return payload, nil
}

type modernDiscoveryResponse struct {
	ErrorCode int `json:"error_code"`
	Result    struct {
		DeviceID    string `json:"device_id"`
		DeviceModel string `json:"device_model"`
		DeviceType  string `json:"device_type"`
		MAC         string `json:"mac"`
		HWVer       string `json:"hw_ver"`
		FWVer       string `json:"fw_ver"`
		Encryption  struct {
			HTTPPort   int    `json:"http_port"`
			Type       string `json:"encrypt_type"`
			LoginLevel int    `json:"lv"`
		} `json:"mgt_encrypt_schm"`
	} `json:"result"`
}

func parseModernDiscoveryResponse(payload []byte) (modernDiscoveryResponse, error) {
	if len(payload) <= 16 {
		return modernDiscoveryResponse{}, errors.New("tapo: discovery response is too short")
	}
	var response modernDiscoveryResponse
	if err := json.Unmarshal(payload[16:], &response); err != nil {
		return modernDiscoveryResponse{}, fmt.Errorf("tapo: decode discovery response: %w", err)
	}
	return response, nil
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
