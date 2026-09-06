package tapo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"
)

type fakeP316M struct {
	t        *testing.T
	listener net.Listener
	closed   chan struct{}
	mu       sync.Mutex
	states   map[string]bool
	aliases  map[string]string
	ledRule  string
}

func newFakeP316M(t *testing.T) *fakeP316M {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeP316M{
		t: t, listener: listener, closed: make(chan struct{}),
		states: make(map[string]bool), aliases: make(map[string]string), ledRule: "always",
	}
	go fake.serve()
	t.Cleanup(func() {
		_ = listener.Close()
		<-fake.closed
	})
	return fake
}

func (f *fakeP316M) serve() {
	defer close(f.closed)
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}
		go f.handle(conn)
	}
}

func (f *fakeP316M) handle(conn net.Conn) {
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

	var response any
	switch envelope.Method {
	case "": // The initial HS300 probe is expected to be rejected by a P316M.
		response = map[string]any{"error_code": -1002, "msg": "UNKNOWN_METHOD"}
	case "get_device_info":
		response = modernTestResponse(map[string]any{
			"nickname": base64.StdEncoding.EncodeToString([]byte("Server strip")),
			"model":    "P316M", "device_id": "p316", "mac": "AA-BB-CC-DD-EE-FF",
			"hw_ver": "1.6", "fw_ver": "1.0.5", "rssi": -45,
		})
	case "get_child_device_list":
		// Return these out of order to verify that physical position wins.
		response = modernTestResponse(map[string]any{"child_device_list": []any{
			map[string]any{"nickname": base64.StdEncoding.EncodeToString([]byte("NAS")), "device_id": "child-2", "device_on": false, "on_time": 0, "position": 2},
			map[string]any{"nickname": base64.StdEncoding.EncodeToString([]byte("Router")), "device_id": "child-1", "device_on": true, "on_time": 90, "position": 1},
		}})
	case "control_child":
		response = f.controlChild(envelope.Params)
	case "get_led_info":
		f.mu.Lock()
		rule := f.ledRule
		f.mu.Unlock()
		response = modernTestResponse(map[string]any{
			"led_rule": rule, "led_status": rule != "never",
			"night_mode": map[string]any{"start_time": 1320, "end_time": 420, "night_mode_type": "custom"},
		})
	case "set_led_info":
		var params map[string]any
		if err := json.Unmarshal(envelope.Params, &params); err != nil {
			f.t.Errorf("decode LED params: %v", err)
		}
		f.mu.Lock()
		f.ledRule, _ = params["led_rule"].(string)
		f.mu.Unlock()
		response = map[string]any{"error_code": 0}
	default:
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

func modernTestResponse(result any) map[string]any {
	return map[string]any{"error_code": 0, "result": result}
}

func (f *fakeP316M) controlChild(payload json.RawMessage) any {
	var params struct {
		DeviceID    string `json:"device_id"`
		RequestData struct {
			Method string `json:"method"`
			Params struct {
				Requests []struct {
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				} `json:"requests"`
			} `json:"params"`
		} `json:"requestData"`
	}
	if err := json.Unmarshal(payload, &params); err != nil {
		f.t.Errorf("decode control_child params: %v", err)
	}
	if params.RequestData.Method != "multipleRequest" || len(params.RequestData.Params.Requests) != 1 {
		f.t.Errorf("unexpected child request envelope: %+v", params.RequestData)
		return map[string]any{"error_code": -1008, "msg": "PARAMS"}
	}
	child := params.RequestData.Params.Requests[0]
	var result any
	switch child.Method {
	case "get_emeter_data":
		if params.DeviceID == "child-1" {
			result = map[string]any{"voltage_mv": 120100, "current_ma": 250, "power_mw": 30025, "energy_wh": 1500}
		} else {
			result = map[string]any{"voltage_mv": 120200, "current_ma": 0, "power_mw": 0, "energy_wh": 250}
		}
	case "set_device_info":
		var values struct {
			DeviceOn *bool  `json:"device_on"`
			Nickname string `json:"nickname"`
		}
		if err := json.Unmarshal(child.Params, &values); err != nil {
			f.t.Errorf("decode set_device_info: %v", err)
		}
		f.mu.Lock()
		if values.DeviceOn != nil {
			f.states[params.DeviceID] = *values.DeviceOn
		}
		if values.Nickname != "" {
			f.aliases[params.DeviceID] = decodeModernString(values.Nickname)
		}
		f.mu.Unlock()
		result = map[string]any{}
	case "get_energy_data":
		var values struct {
			StartTimestamp int64 `json:"start_timestamp"`
			Interval       int   `json:"interval"`
		}
		if err := json.Unmarshal(child.Params, &values); err != nil {
			f.t.Errorf("decode get_energy_data: %v", err)
		}
		data := []int{1250, 500}
		if values.Interval == 43200 {
			data = []int{12500, 9250}
		}
		result = map[string]any{"data": data, "start_timestamp": values.StartTimestamp, "interval": values.Interval}
	default:
		return modernTestControlResponse(-1002, nil)
	}
	return modernTestControlResponse(0, result)
}

func modernTestControlResponse(code int, result any) map[string]any {
	child := map[string]any{"error_code": code}
	if result != nil {
		child["result"] = result
	}
	return modernTestResponse(map[string]any{
		"responseData": map[string]any{"result": map[string]any{"responses": []any{child}}},
	})
}

func (f *fakeP316M) address() string { return f.listener.Addr().String() }

func TestP316MSnapshotAndControl(t *testing.T) {
	fake := newFakeP316M(t)
	strip, err := New(fake.address(), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := strip.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Device.Model != "P316M" || snapshot.Device.Alias != "Server strip" {
		t.Fatalf("unexpected device: %+v", snapshot.Device)
	}
	if len(snapshot.Outlets) != 2 || snapshot.Outlets[0].ID != "child-1" || snapshot.Outlets[0].Alias != "Router" {
		t.Fatalf("unexpected outlets: %+v", snapshot.Outlets)
	}
	if got := snapshot.Outlets[0].Energy.Power; got != 30.025 {
		t.Fatalf("outlet 1 power = %v, want 30.025", got)
	}
	if got := snapshot.Outlets[1].Energy.Total; got != 0.25 {
		t.Fatalf("outlet 2 energy = %v, want 0.25", got)
	}

	if err := strip.SetOutlet(context.Background(), "child-2", true); err != nil {
		t.Fatal(err)
	}
	if err := strip.RenameOutlet(context.Background(), "child-2", "ZimaCube"); err != nil {
		t.Fatal(err)
	}
	if err := strip.SetLED(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	state, alias, ledRule := fake.states["child-2"], fake.aliases["child-2"], fake.ledRule
	fake.mu.Unlock()
	if !state || alias != "ZimaCube" || ledRule != "never" {
		t.Fatalf("state=%t alias=%q led_rule=%q", state, alias, ledRule)
	}
}

func TestP316MEnergyHistory(t *testing.T) {
	fake := newFakeP316M(t)
	strip, err := New(fake.address(), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC)
	days, err := strip.DailyEnergy(context.Background(), "child-1", date)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0].Day != 1 || days[0].Energy != 1.25 || days[1].Energy != 0.5 {
		t.Fatalf("DailyEnergy = %+v", days)
	}
	months, err := strip.MonthlyEnergy(context.Background(), "child-1", 2026)
	if err != nil {
		t.Fatal(err)
	}
	if len(months) != 2 || months[0].Month != time.January || months[0].Energy != 12.5 || months[1].Energy != 9.25 {
		t.Fatalf("MonthlyEnergy = %+v", months)
	}
}
