package tapo

import (
	"context"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"hash/crc32"
	"net"
	"testing"
	"time"
)

func TestDiscover(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	requests := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 4096)
		n, peer, err := server.ReadFromUDP(buf)
		if err != nil {
			return
		}
		requests <- append([]byte{}, buf[:n]...)
		response := []byte(`{"error_code":0,"result":{"device_id":"abc","device_model":"HS300(US)","device_type":"IOT.SMARTPLUGSWITCH","mac":"AA-BB-CC-DD-EE-FF","hw_ver":"2.0","fw_ver":"1.1.6","mgt_encrypt_schm":{"encrypt_type":"KLAP","http_port":80,"lv":2}}}`)
		_, _ = server.WriteToUDP(append(make([]byte, 16), response...), peer)
	}()

	devices, err := discover(context.Background(), 100*time.Millisecond, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("Discover returned %d devices, want 1", len(devices))
	}
	if devices[0].Device.Model != "HS300(US)" || devices[0].Device.DeviceID != "abc" || devices[0].HTTPPort != 80 {
		t.Fatalf("unexpected device: %+v", devices[0])
	}
	select {
	case request := <-requests:
		if len(request) <= 16 || request[0] != 2 || binary.BigEndian.Uint16(request[2:4]) != 1 {
			t.Fatalf("invalid discovery header: %x", request)
		}
		if got, want := int(binary.BigEndian.Uint16(request[4:6])), len(request)-16; got != want {
			t.Fatalf("message size = %d, want %d", got, want)
		}
		gotCRC := binary.BigEndian.Uint32(request[12:16])
		crcInput := append([]byte{}, request...)
		binary.BigEndian.PutUint32(crcInput[12:16], discoveryInitialCRC)
		if wantCRC := crc32.ChecksumIEEE(crcInput); gotCRC != wantCRC {
			t.Fatalf("CRC = %08x, want %08x", gotCRC, wantCRC)
		}
		var body struct {
			Params struct {
				RSAKey string `json:"rsa_key"`
			} `json:"params"`
		}
		if err := json.Unmarshal(request[16:], &body); err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode([]byte(body.Params.RSAKey))
		if block == nil || block.Type != "RSA PUBLIC KEY" {
			t.Fatalf("invalid RSA public key PEM: %q", body.Params.RSAKey)
		}
		key, err := x509.ParsePKCS1PublicKey(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		if key.N.BitLen() != 2048 {
			t.Fatalf("RSA key size = %d, want 2048", key.N.BitLen())
		}
	default:
		t.Fatal("server did not decode a discovery request")
	}
}

func TestDiscoverCanceledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Discover(ctx, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Discover error = %v, want context.Canceled", err)
	}
}

func TestSubnetDestinations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		subnet     string
		wantFirst  string
		wantLast   string
		wantLength int
	}{
		{name: "bare address is slash 24", subnet: "192.168.5.0", wantFirst: "192.168.5.1:20002", wantLast: "192.168.5.254:20002", wantLength: 254},
		{name: "CIDR", subnet: "192.0.2.8/30", wantFirst: "192.0.2.9:20002", wantLast: "192.0.2.10:20002", wantLength: 2},
		{name: "host CIDR is masked", subnet: "192.0.2.11/30", wantFirst: "192.0.2.9:20002", wantLast: "192.0.2.10:20002", wantLength: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destinations, err := subnetDestinations(test.subnet, ModernDiscoveryPort)
			if err != nil {
				t.Fatal(err)
			}
			if len(destinations) != test.wantLength {
				t.Fatalf("got %d destinations, want %d", len(destinations), test.wantLength)
			}
			if got := destinations[0].String(); got != test.wantFirst {
				t.Fatalf("first destination = %q, want %q", got, test.wantFirst)
			}
			if got := destinations[len(destinations)-1].String(); got != test.wantLast {
				t.Fatalf("last destination = %q, want %q", got, test.wantLast)
			}
		})
	}
}

func TestSubnetDestinationsRejectsInvalidOrLargeSubnet(t *testing.T) {
	t.Parallel()
	for _, subnet := range []string{"not-an-address", "2001:db8::/64", "10.0.0.0/8"} {
		if _, err := subnetDestinations(subnet, ModernDiscoveryPort); err == nil {
			t.Errorf("subnetDestinations(%q) unexpectedly succeeded", subnet)
		}
	}
}
