package config

import "testing"

func TestLegacyTransportValueStillLoads(t *testing.T) {
	for _, legacy := range []Protocol{"kcp", "quic", "websocket", "nonsense", ""} {
		tr := Transport{Protocol: legacy, PoolCount: 8}
		tr.ApplyDefaults()
		if err := tr.Validate(); err != nil {
			t.Errorf("a config carrying protocol %q failed to load: %v; an "+
				"upgrade must not take a working router down", legacy, err)
		}
		if tr.Protocol != ProtocolTCP {
			t.Errorf("protocol %q normalised to %q, want tcp", legacy, tr.Protocol)
		}
	}
}
