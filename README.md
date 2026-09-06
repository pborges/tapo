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

- UDP broadcast or subnet discovery with `tapo.Discover`
- strip and outlet state with `Info`, `Outlets`, and `Snapshot`
- single or batch switching with `SetOutlet`, `SetOutletAt`, and `SetOutlets`
- outlet naming and strip LED control
- real-time energy in V, A, W, and kWh
- normalized daily and monthly energy histories
- `Query` as a raw JSON escape hatch

`Snapshot` can return both a partial snapshot and an error if one outlet's
energy meter fails. Check the snapshot before treating the error as fatal.

## Terminal dashboard

Run against one or more known addresses (the flag may also be repeated):

```sh
go run ./cmd/tapo -hosts 192.168.1.42,192.168.1.43
```

For an authenticated KLAP device, pass credentials through the environment so
the password is not exposed in the process list or shell history:

```sh
export TAPO_USERNAME='owner@example.com'
export TAPO_PASSWORD='account-password'
go run ./cmd/tapo -hosts 192.168.1.42
```

Or omit the address to use UDP discovery:

```sh
go run ./cmd/tapo
```

To probe a specific `/24`, pass its network address. CIDR notation is also
accepted (up to a `/16`):

```sh
go run ./cmd/tapo -subnet 192.168.5.0
go run ./cmd/tapo -subnet 192.168.4.0/23
```

From the library, provide the subnet after the timeout:

```go
devices, err := tapo.Discover(ctx, 5*time.Second, "192.168.5.0")
```

The dashboard displays every discovered or configured HS300. Use the arrow
keys (or `j`/`k`) to select outlets across all strips, Space or Enter to
toggle, `r` to refresh all strips, and `q` to quit.

Some firmware versions require enabling local/third-party control in the Tapo
app under **Me → Third-Party Services → Third-Party Compatibility** (or in the
Kasa app under **Me → Settings → Third-Party Compatibility**). Discovery uses
the legacy UDP/9999 protocol; use `-hosts` for KLAP-only devices.
