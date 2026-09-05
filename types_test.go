package tapo

import "testing"

func ptr(value float64) *float64 { return &value }

func TestEnergyNormalization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		wire wireEnergy
		want Energy
	}{
		{
			name: "milli units",
			wire: wireEnergy{VoltageMV: ptr(120123), CurrentMA: ptr(412), PowerMW: ptr(49300), TotalWH: ptr(1234)},
			want: Energy{Voltage: 120.123, Current: .412, Power: 49.3, Total: 1.234},
		},
		{
			name: "base units",
			wire: wireEnergy{Voltage: ptr(121.1), Current: ptr(.5), Power: ptr(60.55), Total: ptr(2.1)},
			want: Energy{Voltage: 121.1, Current: .5, Power: 60.55, Total: 2.1},
		},
		{
			name: "base units take precedence",
			wire: wireEnergy{Power: ptr(3), PowerMW: ptr(9000)},
			want: Energy{Power: 3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.wire.public(); got != tt.want {
				t.Fatalf("public() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
