# tapo

`tapo` is a Go library and terminal dashboard for TP-Link Kasa/Tapo devices,
with first-class support for the HS300 power strip. It automatically supports
the legacy XOR protocol on port 9999 and authenticated KLAP v1/v2 on port 80.
It communicates directly over the LAN; it does not use TP-Link's cloud API.

## Library

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/pborges/tapo"
)

func main() {
	strip, err := tapo.New("192.168.1.42")
	if err != nil {
		log.Fatal(err)
	}

	snapshot, err := strip.Snapshot(context.Background())
	if snapshot == nil {
		log.Fatal(err)
	}
	for _, outlet := range snapshot.Outlets {
		if outlet.Energy != nil {
			fmt.Printf("%s: on=%t power=%.1f W\n", outlet.Alias, outlet.On, outlet.Energy.Power)
		}
	}

	// Child IDs are stable and are returned in every snapshot.
	if err := strip.SetOutlet(context.Background(), snapshot.Outlets[0].ID, true); err != nil {
		log.Fatal(err)
	}
}
```

Newer HS300 firmware using KLAP needs the credentials for the TP-Link account
that owns the device:

```go
strip, err := tapo.New(
	"192.168.1.42",
	tapo.WithCredentials("owner@example.com", "account-password"),
)
```

The main API includes:

- UDP discovery with `tapo.Discover`
- strip and outlet state with `Info`, `Outlets`, and `Snapshot`
- single or batch switching with `SetOutlet`, `SetOutletAt`, and `SetOutlets`
- outlet naming and strip LED control
- real-time energy in V, A, W, and kWh
- normalized daily and monthly energy histories
- `Query` as a raw JSON escape hatch

`Snapshot` can return both a partial snapshot and an error if one outlet's
energy meter fails. Check the snapshot before treating the error as fatal.

## Terminal dashboard

Run against a known address:

```sh
go run ./cmd/tapo -host 192.168.1.42
```

For an authenticated KLAP device, pass credentials through the environment so
the password is not exposed in the process list or shell history:

```sh
export TAPO_USERNAME='owner@example.com'
export TAPO_PASSWORD='account-password'
go run ./cmd/tapo -host 192.168.1.42
```

Or omit the address to use UDP discovery:

```sh
go run ./cmd/tapo
```

Use the arrow keys (or `j`/`k`) to select an outlet, Space or Enter to toggle
it, `r` to refresh, and `q` to quit.

Some firmware versions require enabling local/third-party control in the Tapo
app under **Me → Third-Party Services → Third-Party Compatibility** (or in the
Kasa app under **Me → Settings → Third-Party Compatibility**). Automatic
discovery currently uses the legacy UDP/9999 broadcast; pass `-host` for KLAP
devices or devices on another VLAN.
