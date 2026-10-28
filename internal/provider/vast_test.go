package provider

import (
	"encoding/json"
	"testing"
)

func TestVastInstanceMapping(t *testing.T) {
	raw := `{"id": 42, "actual_status": "running", "cur_state": "running", "public_ipaddr": "1.2.3.4\n",
	  "ports": {"22/tcp": [{"HostIp": "0.0.0.0", "HostPort": "40022"}]}, "ssh_host": "ssh5.vast.ai", "ssh_port": 1234,
	  "gpu_name": "RTX 4090", "dph_total": 0.41, "label": "gpu-backpack"}`
	var vi vastInstance
	if err := json.Unmarshal([]byte(raw), &vi); err != nil {
		t.Fatal(err)
	}
	in := vi.toInstance()
	if in.Status != StatusRunning || in.Host != "1.2.3.4" || in.Port != 40022 || !in.Direct || in.ID != "42" {
		t.Fatalf("direct mapping wrong: %+v", in)
	}

	vi.Ports = nil
	in = vi.toInstance()
	if in.Direct || in.Host != "ssh5.vast.ai" || in.Port != 1234 {
		t.Fatalf("proxy fallback wrong: %+v", in)
	}
}

func TestVastStatuses(t *testing.T) {
	for _, c := range []struct {
		actual, cur string
		want        Status
	}{
		{"running", "running", StatusRunning},
		{"loading", "running", StatusStarting},
		{"", "", StatusStarting},
		{"exited", "stopped", StatusStopped},
		{"offline", "running", StatusStopped},
	} {
		vi := vastInstance{ID: 1, ActualStatus: c.actual, CurState: c.cur}
		if got := vi.toInstance().Status; got != c.want {
			t.Errorf("%s/%s = %v want %v", c.actual, c.cur, got, c.want)
		}
	}
}

func TestBidPriceCapped(t *testing.T) {
	if got := bidPrice(0.20, 0.60); got != 0.23 {
		t.Errorf("got %v", got)
	}
	if got := bidPrice(0.58, 0.60); got != 0.60 {
		t.Errorf("cap: got %v", got)
	}
}
