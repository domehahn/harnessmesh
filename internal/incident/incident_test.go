package incident

import (
	"testing"
	"time"
)

func TestFromPostureCreatesActionableIncidents(t *testing.T) {
	result := FromPosture("tenant-a", 1, 2, 0, time.Unix(1, 0))
	if len(result) != 2 || result[1].Severity != SeverityCritical || result[1].Runbook == "" {
		t.Fatalf("unexpected incidents: %#v", result)
	}
}
