package provider

import (
	"encoding/json"
	"io"
	"sync"
)

// JSONAuditSink emits one redacted structured record per provider request.
// Prompts, tool outputs, authorization headers, and credentials are not part
// of AuditRecord and therefore cannot enter this sink accidentally.
type JSONAuditSink struct {
	mu sync.Mutex
	w  io.Writer
}

func NewJSONAuditSink(w io.Writer) AuditSink {
	if w == nil {
		return noopAuditSink{}
	}
	return &JSONAuditSink{w: w}
}

func (s *JSONAuditSink) Record(record AuditRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record.DurationMS == 0 {
		record.DurationMS = record.LatencyMS
	}
	data, err := json.Marshal(record)
	if err != nil {
		return
	}
	_, _ = s.w.Write(append(data, '\n'))
}
