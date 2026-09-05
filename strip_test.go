package tapo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeDevice struct {
	t         *testing.T
	listener  net.Listener
	mu        sync.Mutex
	lastState int
	active    int
	maxActive int
	delay     time.Duration
	closed    chan struct{}
}

func newFakeDevice(t *testing.T) *fakeDevice {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeDevice{t: t, listener: listener, closed: make(chan struct{})}
	go fake.serve()
	t.Cleanup(func() {
		_ = listener.Close()
		<-fake.closed
	})
	return fake
}

func (f *fakeDevice) serve() {
	defer close(f.closed)
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeDevice) handle(conn net.Conn) {
	defer conn.Close()
	f.mu.Lock()
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	delay := f.delay
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if delay > 0 {
		time.Sleep(delay)
	}
	request, err := readFrame(conn)
	if err != nil {
		return
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(request, &body); err != nil {
		f.t.Errorf("decode request: %v", err)
		return
	}

	var response string
	switch {
	case bytesContain(request, `"get_sysinfo"`):
		response = `{"system":{"get_sysinfo":{"err_code":0,"alias":"Desk strip","model":"HS300(US)","deviceId":"strip-id","mac":"AA:BB:CC:DD:EE:FF","hw_ver":"2.0","sw_ver":"1.0.21","rssi":-47,"children":[{"id":"strip-id00","alias":"Monitor","state":1,"on_time":90},{"id":"strip-id01","alias":"Lamp","state":0,"on_time":0}]}}}`
	case bytesContain(request, `"get_realtime"`):
		if bytesContain(request, `strip-id00`) {
			response = `{"emeter":{"get_realtime":{"err_code":0,"voltage_mv":120100,"current_ma":250,"power_mw":30025,"total_wh":1500}}}`
		} else {
			response = `{"emeter":{"get_realtime":{"err_code":0,"voltage":120.2,"current":0,"power":0,"total":0.25}}}`
		}
	case bytesContain(request, `"get_daystat"`):
		response = `{"emeter":{"get_daystat":{"err_code":0,"day_list":[{"day":1,"energy_wh":1250},{"day":2,"energy_wh":500}]}}}`
	case bytesContain(request, `"get_monthstat"`):
		response = `{"emeter":{"get_monthstat":{"err_code":0,"month_list":[{"month":1,"energy":12.5},{"month":2,"energy":9.25}]}}}`
	case bytesContain(request, `"set_relay_state"`):
		var decoded struct {
			System struct {
				SetRelayState struct {
					State int `json:"state"`
				} `json:"set_relay_state"`
			} `json:"system"`
		}
		if err := json.Unmarshal(request, &decoded); err != nil {
			f.t.Errorf("decode relay request: %v", err)
		}
		f.mu.Lock()
		f.lastState = decoded.System.SetRelayState.State
		f.mu.Unlock()
		response = `{"system":{"set_relay_state":{"err_code":0}}}`
	default:
		response = `{"system":{"unknown":{"err_code":-1,"err_msg":"unsupported"}}}`
	}
	if err := writeFrame(conn, []byte(response)); err != nil {
		f.t.Errorf("write response: %v", err)
	}
}

func bytesContain(data []byte, part string) bool { return strings.Contains(string(data), part) }

func (f *fakeDevice) address() string { return f.listener.Addr().String() }

func TestSnapshotAndSetOutlet(t *testing.T) {
	fake := newFakeDevice(t)
	strip, err := New(fake.address(), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := strip.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Device.Model != "HS300(US)" || len(snapshot.Outlets) != 2 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if got := snapshot.Outlets[0].Energy.Power; got != 30.025 {
		t.Fatalf("outlet 1 power = %v, want 30.025", got)
	}
	if got := snapshot.Outlets[1].Energy.Total; got != .25 {
		t.Fatalf("outlet 2 total = %v, want .25", got)
	}
	if err := strip.SetOutlet(context.Background(), snapshot.Outlets[1].ID, true); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	gotState := fake.lastState
	fake.mu.Unlock()
	if gotState != 1 {
		t.Fatalf("relay state = %d, want 1", gotState)
	}
}

func TestSnapshotSerializesLegacyConnections(t *testing.T) {
	fake := newFakeDevice(t)
	fake.mu.Lock()
	fake.delay = 10 * time.Millisecond
	fake.mu.Unlock()
	strip, err := New(fake.address(), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := strip.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	maxActive := fake.maxActive
	fake.mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("maximum concurrent legacy connections = %d, want 1", maxActive)
	}
}

func TestSetOutletAtRejectsBadIndex(t *testing.T) {
	fake := newFakeDevice(t)
	strip, err := New(fake.address(), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := strip.SetOutletAt(context.Background(), 3, true); err == nil {
		t.Fatal("SetOutletAt accepted invalid index")
	}
}

func TestEnergyHistory(t *testing.T) {
	fake := newFakeDevice(t)
	strip, err := New(fake.address(), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	days, err := strip.DailyEnergy(context.Background(), "strip-id00", time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].Energy != 1.25 || days[1].Day != 2 {
		t.Fatalf("DailyEnergy = %+v", days)
	}
	months, err := strip.MonthlyEnergy(context.Background(), "strip-id00", 2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(months) != 2 || months[0].Month != time.January || months[1].Energy != 9.25 {
		t.Fatalf("MonthlyEnergy = %+v", months)
	}
}

func TestCanceledContext(t *testing.T) {
	strip, err := New("192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := strip.Info(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Info error = %v, want context.Canceled", err)
	}
}

func TestDeviceError(t *testing.T) {
	err := checkDeviceError("emeter", "get_realtime", errorFields{ErrorCode: -1, ErrorMsg: "unsupported"})
	var deviceErr *DeviceError
	if !errors.As(err, &deviceErr) {
		t.Fatalf("error type = %T, want *DeviceError", err)
	}
	if got, want := err.Error(), "tapo: emeter.get_realtime failed (-1): unsupported"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestNewValidation(t *testing.T) {
	tests := []struct {
		name string
		host string
		opts []Option
	}{
		{name: "empty host", host: ""},
		{name: "bad port", host: "localhost", opts: []Option{WithPort(70000)}},
		{name: "bad HTTP port", host: "localhost", opts: []Option{WithHTTPPort(0)}},
		{name: "bad timeout", host: "localhost", opts: []Option{WithTimeout(0)}},
		{name: "partial credentials", host: "localhost", opts: []Option{WithCredentials("owner@example.com", "")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.host, tt.opts...); err == nil {
				t.Fatal("New returned nil error")
			}
		})
	}
}

func ExampleStrip_Snapshot() {
	strip, _ := New("192.0.2.1")
	_ = strip
	fmt.Println("Use Snapshot with a context to read every outlet")
	// Output: Use Snapshot with a context to read every outlet
}
