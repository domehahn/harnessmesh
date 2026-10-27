package incident

import "time"

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type Incident struct {
	ID        string    `json:"id"`
	Tenant    string    `json:"tenant"`
	Severity  Severity  `json:"severity"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Sources   []string  `json:"sources"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Runbook   string    `json:"runbook,omitempty"`
}

func FromPosture(tenant string, failedJobs, deadJobs, unhealthyAgents int, now time.Time) []Incident {
	incidents := []Incident{}
	add := func(id string, severity Severity, title string, count int, runbook string) {
		if count > 0 {
			incidents = append(incidents, Incident{ID: id, Tenant: tenant, Severity: severity, Title: title, Status: "open", Sources: []string{"jobs"}, StartedAt: now.UTC(), UpdatedAt: now.UTC(), Runbook: runbook})
		}
	}
	add("failed-jobs", SeverityWarning, "Failed jobs require investigation", failedJobs, "inspect-job-timeline")
	add("dead-letters", SeverityCritical, "Dead-letter jobs require remediation", deadJobs, "replay-after-remediation")
	add("unhealthy-agents", SeverityCritical, "Agent health degradation detected", unhealthyAgents, "quarantine-and-recover")
	return incidents
}
