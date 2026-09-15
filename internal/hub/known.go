package hub

import (
	"strings"

	"github.com/marstid/goznp/pkg/zcl"
)

// Kind is a coarse UI/control class for a device.
type Kind string

const (
	KindLight   Kind = "light"
	KindPlug    Kind = "plug"
	KindSwitch  Kind = "switch"
	KindSensor  Kind = "sensor"
	KindMotion  Kind = "motion"
	KindUnknown Kind = "unknown"
)

func ValidKind(k Kind) bool {
	switch k {
	case KindLight, KindPlug, KindSwitch, KindSensor, KindMotion, KindUnknown:
		return true
	default:
		return false
	}
}

type knownDevice struct {
	Name string
	Kind Kind
}

// Optional IEEE → name/kind hints. Prefer user labels in data/devices.json.
var knownByIEEE = map[string]knownDevice{}

func lookupKnown(ieee string) (knownDevice, bool) {
	k, ok := knownByIEEE[compactIEEE(ieee)]
	return k, ok
}

func hasCluster(ids []uint16, id zcl.ClusterID) bool {
	for _, c := range ids {
		if c == uint16(id) {
			return true
		}
	}
	return false
}

func inferKind(manufacturer, model string, inClusters []uint16) Kind {
	if model == "RB 267" || manufacturer == "innr" {
		return KindLight
	}
	if strings.HasPrefix(model, "lumi.motion") || strings.HasPrefix(model, "lumi.sensor_motion") {
		return KindMotion
	}
	if manufacturer == "LUMI" && strings.Contains(model, "motion") {
		return KindMotion
	}
	if hasCluster(inClusters, zcl.ClusterOccupancySensing) || hasCluster(inClusters, zcl.ClusterIASZone) {
		return KindMotion
	}
	if hasCluster(inClusters, zcl.ClusterTempMeasurement) || hasCluster(inClusters, zcl.ClusterHumidityMeas) {
		return KindSensor
	}
	if hasCluster(inClusters, zcl.ClusterOnOff) && hasCluster(inClusters, zcl.ClusterLevelControl) {
		return KindLight
	}
	if hasCluster(inClusters, zcl.ClusterOnOff) {
		return KindPlug
	}
	return KindUnknown
}

func clusterName(id uint16) string {
	return zcl.ClusterID(id).String()
}
