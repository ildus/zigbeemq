# AGENTS.md

Rules for agents in this repo. README — how to run.

## Hardware

- Sonoff CC2652 stick on ZNP (`goznp`). Port is usually `/dev/ttyUSB0`.
- **Do not call FormNetwork, factory reset, or `NvStartupOption` clear.** The network already exists (channel 11). A reset wipes every device on it.
- **Do not send on/off/brightness to lamps or plugs unless the user explicitly asks.** Test “unavailable” only on a device that is known to be powered off, never on live lights.
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

## UI

- Primary UI and code language is English. Strings in `index.html` (`en` / `ru` / `de`), choice in `localStorage` (`zigbeemq.lang`).
- Switch card: `click: <type> · HH:MM:SS`, no click counter. Time with seconds from `last_seen_ms`.
- Motion card: `motion detected · HH:MM:SS` when occupied.
- Do not overwrite a fresh click with a stale GET. API: `Cache-Control: no-store`.
- Unavailability is text on the card, no red border. API code: `unavailable`.

## MQTT

- Base `zigbee2mqtt`, broker `tcp://bb:1883`. Do not publish retained junk on foreign names. Client id `zigbeemq`.

## Workflow

- Change only what the task needs. Do not drive-by refactor.
- Do not commit until asked.
- Restart on a PC: build to `/tmp/zigbeemq.bin` or `./zigbeemq`, kill the old pid, pause 2–3 s, run `-port /dev/ttyUSB0 -listen 127.0.0.1:8088 -data <repo>/data -mqtt tcp://bb:1883`.
- On the router: `./scripts/deploy-openwrt.sh` (SSH `router`, arm64, `/etc/init.d/zigbeemq`). No FormNetwork.
- Click logs: `incoming` / `click` / `switch bound`. If clicks are missing from the log, it is radio/waiter, not “just UI”.
- On OpenWrt logs: `logread -e zigbeemq`.
