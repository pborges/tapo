package tapo

import (
	"context"
	"encoding/json"
	"errors"
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

	requests := make(chan map[string]json.RawMessage, 1)
	go func() {
		buf := make([]byte, 4096)
		n, peer, err := server.ReadFromUDP(buf)
		if err != nil {
			return
		}
		var request map[string]json.RawMessage
		if err := json.Unmarshal(decrypt(buf[:n]), &request); err != nil {
			return
		}
		requests <- request
		response := []byte(`{"system":{"get_sysinfo":{"err_code":0,"alias":"Test strip","model":"HS300(US)","deviceId":"abc","children":[{"id":"abc00","alias":"One","state":1,"on_time":10}]}}}`)
		_, _ = server.WriteToUDP(encrypt(response), peer)
	}()

	devices, err := discover(context.Background(), 100*time.Millisecond, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 {
		t.Fatalf("Discover returned %d devices, want 1", len(devices))
	}
	if devices[0].Device.Model != "HS300(US)" || len(devices[0].Outlets) != 1 {
		t.Fatalf("unexpected device: %+v", devices[0])
	}
	select {
	case request := <-requests:
		if _, ok := request["system"]; !ok {
			t.Fatal("discovery request is missing system module")
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
		{name: "bare address is slash 24", subnet: "192.168.5.0", wantFirst: "192.168.5.1:9999", wantLast: "192.168.5.254:9999", wantLength: 254},
		{name: "CIDR", subnet: "192.0.2.8/30", wantFirst: "192.0.2.9:9999", wantLast: "192.0.2.10:9999", wantLength: 2},
		{name: "host CIDR is masked", subnet: "192.0.2.11/30", wantFirst: "192.0.2.9:9999", wantLast: "192.0.2.10:9999", wantLength: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destinations, err := subnetDestinations(test.subnet, DefaultPort)
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
		if _, err := subnetDestinations(subnet, DefaultPort); err == nil {
			t.Errorf("subnetDestinations(%q) unexpectedly succeeded", subnet)
		}
	}
}
