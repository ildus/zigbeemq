package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/ildus/zigbeemq/internal/hub"
)

type Config struct {
	Broker   string
	Base     string
	ClientID string
}

type Bridge struct {
	log *slog.Logger
	hub *hub.Hub
	cfg Config
	cli mqtt.Client
}

func New(log *slog.Logger, h *hub.Hub, cfg Config) *Bridge {
	if cfg.Base == "" {
		cfg.Base = "zigbee2mqtt"
	}
	if cfg.ClientID == "" {
		cfg.ClientID = "zigbeemq"
	}
	return &Bridge{log: log, hub: h, cfg: cfg}
}

func (b *Bridge) Run(ctx context.Context) error {
	opts := mqtt.NewClientOptions()
	opts.AddBroker(b.cfg.Broker)
	opts.SetClientID(b.cfg.ClientID)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(3 * time.Second)
	opts.SetKeepAlive(30 * time.Second)
	opts.SetOrderMatters(false)
	opts.SetWill(b.cfg.Base+"/bridge/state", `{"state":"offline"}`, 1, true)
	opts.OnConnect = func(c mqtt.Client) {
		b.log.Info("mqtt connected", "broker", b.cfg.Broker)
		b.publishRaw(b.cfg.Base+"/bridge/state", []byte(`{"state":"online"}`), true)
		if tok := c.Subscribe(b.cfg.Base+"/+/set", 0, b.onSet); tok.Wait() && tok.Error() != nil {
			b.log.Warn("mqtt subscribe", "err", tok.Error())
		}
		for _, d := range b.hub.Devices() {
			b.publishDevice(d)
		}
	}
	opts.OnConnectionLost = func(_ mqtt.Client, err error) {
		b.log.Warn("mqtt lost", "err", err)
	}

	b.cli = mqtt.NewClient(opts)
	if tok := b.cli.Connect(); tok.Wait() && tok.Error() != nil {
		return fmt.Errorf("mqtt connect: %w", tok.Error())
	}

	events, unsub := b.hub.Subscribe()
	defer unsub()
	defer func() {
		b.publishRaw(b.cfg.Base+"/bridge/state", []byte(`{"state":"offline"}`), true)
		b.cli.Disconnect(250)
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			switch ev.Type {
			case "device":
				if d, ok := ev.Data.(hub.Device); ok {
					b.publishDevice(d)
				}
			case "devices":
				if list, ok := ev.Data.([]hub.Device); ok {
					for _, d := range list {
						b.publishDevice(d)
					}
				}
			case "renamed":
				if r, ok := ev.Data.(hub.RenamedEvent); ok {
					b.unpublish(r.From)
				}
			case "removed":
				if r, ok := ev.Data.(hub.RemovedEvent); ok {
					b.unpublish(r.Name)
					b.unpublish(r.IEEE)
				}
			}
		}
	}
}

func (b *Bridge) onSet(_ mqtt.Client, msg mqtt.Message) {
	go b.handleSet(msg.Topic(), msg.Payload())
}

func (b *Bridge) handleSet(topic string, payload []byte) {
	name, ok := setName(b.cfg.Base, topic)
	if !ok {
		return
	}
	d, err := b.hub.DeviceByName(name)
	if err != nil {
		b.log.Warn("mqtt set unknown", "topic", topic)
		return
	}

	state, brightness, err := parseSet(payload)
	if err != nil {
		b.log.Warn("mqtt set payload", "err", err, "payload", string(payload))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if brightness != nil {
		if err := b.hub.SetBrightness(ctx, d.IEEE, *brightness); err != nil {
			if hub.IsUnavailable(err) {
				b.log.Info("mqtt brightness unavailable", "name", name)
			} else {
				b.log.Warn("mqtt brightness", "name", name, "err", err)
			}
		}
	}
	if state != nil && brightness == nil {
		on := *state
		if strings.EqualFold(*state, "toggle") {
			cur := d.On != nil && *d.On
			on = boolString(!cur)
		}
		if err := b.hub.Turn(ctx, d.IEEE, strings.EqualFold(on, "on") || on == "1" || on == "true"); err != nil {
			if hub.IsUnavailable(err) {
				b.log.Info("mqtt turn unavailable", "name", name)
			} else {
				b.log.Warn("mqtt turn", "name", name, "err", err)
			}
		}
	}
}

func boolString(on bool) string {
	if on {
		return "ON"
	}
	return "OFF"
}

func setName(base, topic string) (string, bool) {
	prefix := strings.TrimSuffix(base, "/") + "/"
	if !strings.HasPrefix(topic, prefix) || !strings.HasSuffix(topic, "/set") {
		return "", false
	}
	rest := strings.TrimPrefix(topic, prefix)
	if !strings.HasSuffix(rest, "/set") {
		return "", false
	}
	mid := strings.TrimSuffix(rest, "/set")
	if mid == "" || strings.Contains(mid, "/") {
		return "", false
	}
	return mid, true
}

func parseSet(payload []byte) (state *string, percent *uint8, err error) {
	s := strings.TrimSpace(string(payload))
	if s == "" {
		return nil, nil, fmt.Errorf("empty")
	}
	switch strings.ToUpper(s) {
	case "ON", "OFF", "TOGGLE":
		u := strings.ToUpper(s)
		return &u, nil, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, nil, err
	}
	if v, ok := raw["state"]; ok {
		u := strings.ToUpper(fmt.Sprint(v))
		state = &u
	}
	if v, ok := raw["brightness_percent"]; ok {
		p := uint8(clamp(toFloat(v), 0, 100))
		percent = &p
	} else if v, ok := raw["brightness"]; ok {
		p := uint8(math.Round(clamp(toFloat(v), 0, 254) / 254 * 100))
		percent = &p
	}
	return state, percent, nil
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		var f float64
		_, _ = fmt.Sscanf(fmt.Sprint(v), "%f", &f)
		return f
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (b *Bridge) publishDevice(d hub.Device) {
	body := map[string]any{}
	if d.On != nil {
		if *d.On {
			body["state"] = "ON"
		} else {
			body["state"] = "OFF"
		}
	}
	if d.Brightness != nil {
		body["brightness"] = int(math.Round(float64(*d.Brightness) / 100 * 254))
	}
	if d.LastClick != "" {
		body["action"] = d.LastClick
	}
	if d.Occupancy != nil {
		body["occupancy"] = *d.Occupancy
	}
	if len(body) == 0 {
		return
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return
	}
	name := d.Name
	if name == "" {
		name = d.IEEE
	}
	b.publishRaw(b.cfg.Base+"/"+name, raw, true)
	if d.LastClick != "" {
		b.publishRaw(b.cfg.Base+"/"+name+"/action", []byte(d.LastClick), false)
		go func(topic string, snapshot map[string]any) {
			time.Sleep(150 * time.Millisecond)
			snapshot["action"] = ""
			raw, err := json.Marshal(snapshot)
			if err != nil {
				return
			}
			b.publishRaw(topic, raw, true)
		}(b.cfg.Base+"/"+name, body)
	}
}

func (b *Bridge) unpublish(name string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	b.publishRaw(b.cfg.Base+"/"+name, []byte{}, true)
}

func (b *Bridge) publishRaw(topic string, payload []byte, retain bool) {
	if b.cli == nil || !b.cli.IsConnected() {
		return
	}
	tok := b.cli.Publish(topic, 0, retain, payload)
	tok.Wait()
	if err := tok.Error(); err != nil {
		b.log.Warn("mqtt publish", "topic", topic, "err", err)
	}
}
