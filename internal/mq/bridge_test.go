package mq

import (
	"encoding/json"
	"testing"
)

func TestSetName(t *testing.T) {
	cs := []struct {
		b, t, w string
		o       bool
	}{{"zigbee2mqtt", "zigbee2mqtt/lamp/set", "lamp", true}, {"zigbee2mqtt/", "zigbee2mqtt/lamp/set", "lamp", true}, {"zigbee2mqtt", "zigbee2mqtt/set", "", false}, {"zigbee2mqtt", "zigbee2mqtt/room/lamp/set", "", false}, {"zigbee2mqtt", "other/lamp/set", "", false}}
	for _, x := range cs {
		g, o := setName(x.b, x.t)
		if g != x.w || o != x.o {
			t.Errorf("got (%q,%v)", g, o)
		}
	}
}
func TestParseSet(t *testing.T) {
	cs := []struct {
		p, s string
		hasS bool
		pct  int
		bad  bool
	}{{" on ", "ON", true, -1, false}, {"TOGGLE", "TOGGLE", true, -1, false}, {"{\"state\":\"off\",\"brightness_percent\":42}", "OFF", true, 42, false}, {"{\"brightness\":127}", "", false, 50, false}, {"{\"brightness_percent\":150}", "", false, 100, false}, {"", "", false, -1, true}, {"nope", "", false, -1, true}}
	for _, x := range cs {
		s, p, e := parseSet([]byte(x.p))
		if (e != nil) != x.bad {
			t.Errorf("%q err=%v", x.p, e)
			continue
		}
		if (s != nil) != x.hasS || (s != nil && *s != x.s) {
			t.Errorf("%q state=%v", x.p, s)
		}
		if x.pct < 0 && p != nil || x.pct >= 0 && (p == nil || int(*p) != x.pct) {
			t.Errorf("%q pct=%v", x.p, p)
		}
	}
}
func TestNumericHelpers(t *testing.T) {
	if toFloat(json.Number("12.5")) != 12.5 || toFloat("7.25") != 7.25 {
		t.Fatal("toFloat")
	}
	if clamp(-1, 0, 10) != 0 || clamp(11, 0, 10) != 10 || clamp(5, 0, 10) != 5 {
		t.Fatal("clamp")
	}
	if boolString(true) != "ON" || boolString(false) != "OFF" {
		t.Fatal("boolString")
	}
}
