package certinventory

import "testing"

func TestProbeTLSConfigDoesFullHandshakes(t *testing.T) {
	var captured int

	cfg := probeTLSConfig("redis.bosh", func(raw [][]byte) { captured = len(raw) })

	if !cfg.SessionTicketsDisabled {
		t.Error("session tickets are enabled, so a resumed session could skip the served certificate")
	}

	if cfg.ClientSessionCache != nil {
		t.Error("a client session cache is set, so a resumed session could skip the served certificate")
	}

	if cfg.ServerName != "redis.bosh" {
		t.Errorf("server name is %q, want redis.bosh", cfg.ServerName)
	}

	if err := cfg.VerifyPeerCertificate([][]byte{{1}, {2}}, nil); err != nil {
		t.Fatal(err)
	}

	if captured != 2 {
		t.Errorf("captured %d certificates, want 2", captured)
	}
}
