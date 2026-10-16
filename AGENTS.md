# AGENTS.md

Rules for agents in this repo. README — how to run.

## Hardware

- Sonoff CC2652 stick on ZNP (`goznp`). Port is usually `/dev/ttyUSB0`.
- **Do not call FormNetwork, factory reset, or `NvStartupOption` clear.** The network already exists (channel 11). A reset wipes every device on it.
- **Do not send on/off/brightness to lamps, plugs, or relays unless the user explicitly asks.** Test “unavailable” only on a device that is known to be powered off, never on live lights. Saving relay settings the user edited is fine; do not fire inching from Save.
- The stick is exclusive. Before starting a new binary, stop the old `zigbeemq` and wait for serial release (`Serial port busy`).
- Do not run two processes on the same `/dev/ttyUSB0`, and do not run zigbee2mqtt in parallel.

## Stack

- Go, HTTP stdlib, UI — vanilla `internal/web/static/index.html`, no Svelte/React.
- After HTML edits: `go build` (prefer `-a`); the page is `go:embed`. Otherwise the UI stays stale.
- State: `data/devices.json`. Do not commit the `data/` directory.
- Known names: `internal/hub/known.go`. Add IEEE entries only as optional labels, not as the sole source of truth.

## Radio and goznp

- `WaitForIncomingMsg` takes **all** AF IncomingMsg; unmatched nwk/cluster frames are **dropped**. Interview / read attributes on a lamp steal button clicks. Do not leave permanent waiters; do not poll `/api/network` often (holds the radio mutex).
- Incoming clicks: `OnFrame` → queue `h.incoming` (not `go handleIncoming` per frame). Coordinator ep1 must have **OnOff as input**; `AfRegister` succeeds only with status `0`, not `0xB8 AlreadyRegistered`.
- Xiaomi WXKG01LM: press/release on genOnOff, multi-click in attr `0x8000`. Confirm a single click after a short pause (wait for double). Bind OnOff to the coordinator while the button is awake. Do not treat attr `0x8000` = 0/1 as `many`.
- Unavailability: `0xB7`, `0xCD`, timeout → `unavailable` on the card, not an alert with raw status.
- `%X` on goznp `ClusterID`, `Status`, and `DataType` prints the hex of `String()` (`OnOff` becomes `4F6E4F6666`). Compare status with `zcl.StatusSuccess`; log the numeric id.

## Relay (SONOFF ZBMINIR2)

- Kind `relay` is only for model `ZBMINIR2` or white-label `MINI-ZBD`. An OnOff socket stays `plug` and does not get this dialog. Do not rename every plug to relay. Cache rows of a mini still stored as `plug` migrate to `relay` on load.
- Edit dialog: Save writes name, kind, and the whole relay form. `inching_control_set` is a one-shot pulse on its own Set button.
- JSON and MQTT keys stay the zigbee2mqtt names (`power_on_behavior`, `turbo_mode`, `detach_relay_mode`, …). Visible labels are translated (`en` / `ru` / `de`); do not show the raw keys as the label. `turbo_mode` is stronger TX power. `detach_relay_mode` means the wall switch no longer toggles the load.
- Reads and writes use `relayExchange` (`OnFrame`, AF `0x81` and `0x82`, up to 8s). goznp `WaitForIncomingMsg` only matches `0x81` and caps at 5s, so `startUpOnOff` and cluster `0xFC11` never arrive there. Read eWeLink attributes one at a time; a multi-read returns a subset. If a plain read fails, retry with manufacturer `0x1286`. Apply the startup result and the eWeLink result separately — `||` skips eWeLink when startup succeeded.
- Cold boot is several seconds of firmware. No setting shortens it. `delayed_power_on_*` adds a wait before the load; `turbo_mode` applies only after the radio is up.
- OTA is not implemented.

## UI

- Primary UI and code language is English. Strings in `index.html` (`en` / `ru` / `de`), choice in `localStorage` (`zigbeemq.lang`).
- Device list filter: Active / Archived / All (`localStorage` `zigbeemq.filter`). Archive from the edit dialog; device stays on the network and in MQTT.
- Switch card: `click: <type> · HH:MM:SS`, no click counter. Time with seconds from `last_seen_ms`.
- Motion card: `motion detected · HH:MM:SS` when occupied.
- Do not overwrite a fresh click with a stale GET. API: `Cache-Control: no-store`.
- Unavailability is text on the card, no red border. API code: `unavailable`.
- Archived devices: skip background interview / recovery probes; still controllable if shown via All / Archived.

## MQTT

- Base `zigbee2mqtt`, broker `tcp://bb:1883`. Do not publish retained junk on foreign names. Client id `zigbeemq`.

## Workflow

- Change only what the task needs. Do not drive-by refactor.
- After Go edits: `gofmt -w` on changed files. CI fails on `gofmt -l .`.
- Do not commit until asked.
- Restart on a PC: build to `/tmp/zigbeemq.bin` or `./zigbeemq`, kill the old pid, pause 2–3 s, run `-port /dev/ttyUSB0 -listen 127.0.0.1:8088 -data <repo>/data -mqtt tcp://bb:1883`.
- On the router: `env -u GOOS -u GOARCH ./scripts/deploy-openwrt.sh` from this repo (SSH `router`, arm64, `/etc/init.d/zigbeemq`). No FormNetwork. If the old process ignores SIGTERM, `kill -9` before copying (`Text file busy`). `scp` to the router needs `-O`.
- Hit the router API with curl from this PC (`http://192.168.1.1:8080`). OpenWrt `wget` cannot PATCH. An `nc` request that closes stdin cancels the handler (`context canceled`).
- Click logs: `incoming` / `click` / `switch bound`. If clicks are missing from the log, it is radio/waiter, not “just UI”.
- On OpenWrt logs: `logread -e zigbeemq`.
