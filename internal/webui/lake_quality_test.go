package webui

import (
	"github.com/IDisposable/docker-wyze-bridge/internal/go2rtcmgr"
	"testing"
)

func TestQualityProbeRequiresExistingSharedNativeProducer(t *testing.T) {
	active := func() map[string]*go2rtcmgr.StreamInfo {
		return map[string]*go2rtcmgr.StreamInfo{
			"front_lake": {Producers: []go2rtcmgr.ProducerInfo{{ID: 1, BytesRecv: 100}}, Consumers: []go2rtcmgr.ConsumerInfo{{}}},
			"front_4k":   {Producers: []go2rtcmgr.ProducerInfo{{ID: 2, BytesRecv: 200}}, Consumers: []go2rtcmgr.ConsumerInfo{{}}},
		}
	}
	if ids := qualityProducerIDs(active(), "front"); len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatal("active producer not recognized")
	}
	for _, mutate := range []func(map[string]*go2rtcmgr.StreamInfo){
		func(s map[string]*go2rtcmgr.StreamInfo) { delete(s, "front_lake") },
		func(s map[string]*go2rtcmgr.StreamInfo) { s["front_4k"].Consumers = nil },
		func(s map[string]*go2rtcmgr.StreamInfo) { s["front_lake"].Producers = nil },
		func(s map[string]*go2rtcmgr.StreamInfo) { s["front_lake"].Producers[0].BytesRecv = 0 },
		func(s map[string]*go2rtcmgr.StreamInfo) {
			s["front_4k"].Producers = append(s["front_4k"].Producers, go2rtcmgr.ProducerInfo{ID: 3})
		},
	} {
		s := active()
		mutate(s)
		if qualityProducerIDs(s, "front") != nil {
			t.Fatal("probe could activate an absent, idle or ambiguous receiver")
		}
	}
}
