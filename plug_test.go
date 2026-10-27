package tapo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type fakeP110M struct {
	t        *testing.T
	listener net.Listener
	closed   chan struct{}
	noEmeter bool
	mu       sync.Mutex
	on       bool
	alias    string
	methods  map[string]int
}

func newFakeP110M(t *testing.T, noEmeter bool) *fakeP110M {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeP110M{
		t: t, listener: listener, closed: make(chan struct{}), noEmeter: noEmeter,
		alias: "Grow light", methods: make(map[string]int),
	}
	go fake.serve()
	t.Cleanup(func() {
		_ = listener.Close()
		<-fake.closed
	})
	return fake
}

func (f *fakeP110M) serve() {
	defer close(f.closed)
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeP110M) handle(conn net.Conn) {
	defer conn.Close()
	payload, err := readFrame(conn)
	if err != nil {
		return
	}
	var envelope struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		f.t.Errorf("decode request: %v", err)
		return
	}
	f.mu.Lock()
	f.methods[envelope.Method]++
	f.mu.Unlock()

	var response any
	switch envelope.Method {
	case "get_device_info":
		f.mu.Lock()
		response = modernTestResponse(map[string]any{
			"nickname": base64.StdEncoding.EncodeToString([]byte(f.alias)),
			"model":    "P110M", "device_id": "p110", "mac": "11-22-33-44-55-66",
			"hw_ver": "1.0", "fw_ver": "1.2.3", "rssi": -52, "type": "SMART.TAPOPLUG",
			"device_on": f.on, "on_time": 3600,
		})
		f.mu.Unlock()
	case "component_nego":
		energyVersion := 2
		if f.noEmeter {
			energyVersion = 1
		}
		response = modernTestResponse(map[string]any{"component_list": []any{
			map[string]any{"id": "device", "ver_code": 2},
			map[string]any{"id": "energy_monitoring", "ver_code": energyVersion},
			map[string]any{"id": "led", "ver_code": 1},
			map[string]any{"id": "matter", "ver_code": 2},
		}})
	case "get_emeter_data":
		if f.noEmeter {
			response = map[string]any{"error_code": -1002, "msg": "UNKNOWN_METHOD"}
			break
		}
		response = modernTestResponse(map[string]any{"voltage_mv": 121500, "current_ma": 410, "power_mw": 48250, "energy_wh": 3200})
	case "get_energy_usage":
		response = modernTestResponse(map[string]any{"today_energy": 120, "month_energy": 2400, "current_power": 48250})
	case "set_device_info":
		var values struct {
			DeviceOn *bool  `json:"device_on"`
			Nickname string `json:"nickname"`
		}
		if err := json.Unmarshal(envelope.Params, &values); err != nil {
			f.t.Errorf("decode set_device_info: %v", err)
		}
		f.mu.Lock()
		if values.DeviceOn != nil {
			f.on = *values.DeviceOn
		}
		if values.Nickname != "" {
			f.alias = decodeModernString(values.Nickname)
		}
		f.mu.Unlock()
		response = map[string]any{"error_code": 0}
	case "get_energy_data":
		var values struct {
			StartTimestamp int64 `json:"start_timestamp"`
			Interval       int   `json:"interval"`
		}
		if err := json.Unmarshal(envelope.Params, &values); err != nil {
			f.t.Errorf("decode get_energy_data: %v", err)
		}
		data := []int{750, 1000}
		if values.Interval == 43200 {
			data = []int{30000}
		}
		response = modernTestResponse(map[string]any{"data": data, "start_timestamp": values.StartTimestamp, "interval": values.Interval})
	default:
		// get_child_device_list, control_child and the legacy probe are all
		// unknown to a single plug.
		response = map[string]any{"error_code": -1002, "msg": "UNKNOWN_METHOD"}
	}

	wire, err := json.Marshal(response)
	if err != nil {
		f.t.Errorf("encode response: %v", err)
		return
	}
	if err := writeFrame(conn, wire); err != nil {
		f.t.Errorf("write response: %v", err)
	}
}

func (f *fakeP110M) address() string { return f.listener.Addr().String() }

func TestP110MSnapshotAndControl(t *testing.T) {
	fake := newFakeP110M(t, false)
	plug, err := New(fake.address(), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := plug.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Device.Model != "P110M" || snapshot.Device.Alias != "Grow light" {
		t.Fatalf("unexpected device: %+v", snapshot.Device)
	}
	if len(snapshot.Outlets) != 1 {
		t.Fatalf("got %d outlets, want 1: %+v", len(snapshot.Outlets), snapshot.Outlets)
	}
	outlet := snapshot.Outlets[0]
	if outlet.ID != "p110" || outlet.Alias != "Grow light" || outlet.On || outlet.OnTime != time.Hour {
		t.Fatalf("unexpected outlet: %+v", outlet)
	}
	want := Energy{Voltage: 121.5, Current: 0.41, Power: 48.25, Total: 3.2}
	if outlet.Energy == nil || *outlet.Energy != want {
		t.Fatalf("energy = %+v, want %+v", outlet.Energy, want)
	}

	if err := plug.SetOutletAt(context.Background(), 0, true); err != nil {
		t.Fatal(err)
	}
	if err := plug.RenameOutlet(context.Background(), "p110", "Heater"); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	on, alias := fake.on, fake.alias
	controlChild := fake.methods["control_child"] + fake.methods["get_child_device_list"]
	fake.mu.Unlock()
	if !on || alias != "Heater" {
		t.Fatalf("on=%t alias=%q", on, alias)
	}
	if controlChild != 0 {
		t.Fatalf("plug received %d child requests", controlChild)
	}

	if err := plug.SetOutlet(context.Background(), "child-1", false); err == nil {
		t.Fatal("SetOutlet with a foreign ID succeeded")
	}
	var deviceErr *DeviceError
	if err := plug.SetOutlet(context.Background(), "child-1", false); errors.As(err, &deviceErr) {
		t.Fatalf("foreign ID was sent to the device: %v", err)
	}
}

func TestP110MPowerOnlyFirmware(t *testing.T) {
	fake := newFakeP110M(t, true)
	plug, err := New(fake.address(), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		energy, err := plug.Energy(context.Background(), "p110")
		if err != nil {
			t.Fatal(err)
		}
		if energy != (Energy{Power: 48.25}) {
			t.Fatalf("energy = %+v", energy)
		}
	}
	fake.mu.Lock()
	emeterCalls := fake.methods["get_emeter_data"]
	fake.mu.Unlock()
	if emeterCalls != 1 {
		t.Fatalf("get_emeter_data called %d times, want 1", emeterCalls)
	}
}

func TestP110MEnergyHistory(t *testing.T) {
	fake := newFakeP110M(t, false)
	plug, err := New(fake.address(), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC)
	days, err := plug.DailyEnergy(context.Background(), "p110", date)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].Day != 1 || days[0].Energy != 0.75 || days[1].Energy != 1 {
		t.Fatalf("DailyEnergy = %+v", days)
	}
	months, err := plug.MonthlyEnergy(context.Background(), "p110", 2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(months) != 1 || months[0].Month != time.January || months[0].Energy != 30 {
		t.Fatalf("MonthlyEnergy = %+v", months)
	}
}
