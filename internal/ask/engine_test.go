package ask

import "testing"

func TestSelectProbesPortLinux(t *testing.T) {
	probes := selectProbes("what process is using port 3000", "linux")
	foundSS := false
	for _, p := range probes {
		if p.name == "ss" {
			foundSS = true
		}
	}
	if !foundSS {
		t.Fatalf("expected linux probe to include ss, got %+v", probes)
	}
}

func TestSelectProbesDisk(t *testing.T) {
	probes := selectProbes("why is disk usage high", "darwin")
	foundDF := false
	for _, p := range probes {
		if p.name == "df" {
			foundDF = true
		}
	}
	if !foundDF {
		t.Fatalf("expected disk probe to include df, got %+v", probes)
	}
}
