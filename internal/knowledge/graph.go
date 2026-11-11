package knowledge

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/domehahn/harnessmesh/internal/contextpack"
)

// GraphNode represents a typed entity extracted by the Cognee-inspired ECL
// (Extract, Cognify, Load) knowledge pipeline.
type GraphNode struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"` // agent, file, topic, decision, finding, memory_block, record
	Label      string         `json:"label"`
	Confidence float64        `json:"confidence,omitempty"`
	Verified   bool           `json:"verified,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// GraphEdge represents a directed, typed relation between two GraphNodes.
type GraphEdge struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Relation   string  `json:"relation"` // AUTHORED, ADDRESSED_TO, MENTIONS_AGENT, AFFECTS_FILE, TAGGED_WITH, SUPERSEDES
	Weight     float64 `json:"weight"`
	EvidenceID string  `json:"evidence_id,omitempty"`
}

// Graph is the deterministic entity-relationship view derived from knowledge
// archive records.
type Graph struct {
	Nodes   []GraphNode    `json:"nodes"`
	Edges   []GraphEdge    `json:"edges"`
	Summary map[string]int `json:"summary"`
}

var (
	mentionPattern  = regexp.MustCompile(`(?:^|[\s(])@([a-zA-Z0-9_-]{2,64})`)
	filePathPattern = regexp.MustCompile(`(?i)\b((?:[a-z0-9_.-]+/)+[a-z0-9_.-]+\.(?:go|py|ts|tsx|js|jsx|md|json|yaml|yml|toml|sql|sh)|[a-z0-9_.-]+\.(?:go|py|ts|tsx|md|json|yaml|yml|sql))\b`)
)

// Cognify runs the deterministic ECL (Extract, Cognify, Load) pipeline over
// archive records, extracting agents, file scopes, tags, decisions and
// supersession chains into a deduplicated knowledge graph.
func Cognify(records []Record) Graph {
	nodesByID := make(map[string]GraphNode)
	type edgeKey struct {
		from, to, rel string
	}
	edgesByKey := make(map[edgeKey]GraphEdge)

	upsertNode := func(n GraphNode) {
		if existing, ok := nodesByID[n.ID]; ok {
			if n.Confidence > existing.Confidence {
				existing.Confidence = n.Confidence
			}
			if n.Verified {
				existing.Verified = true
			}
			nodesByID[n.ID] = existing
			return
		}
		nodesByID[n.ID] = n
	}

	upsertEdge := func(from, to, rel, evidenceID string, weight float64) {
		if from == "" || to == "" || from == to {
			return
		}
		if weight <= 0 {
			weight = 1.0
		}
		k := edgeKey{from: from, to: to, rel: rel}
		if existing, ok := edgesByKey[k]; ok {
			existing.Weight += weight
			edgesByKey[k] = existing
			return
		}
		edgesByKey[k] = GraphEdge{
			From:       from,
			To:         to,
			Relation:   rel,
			Weight:     weight,
			EvidenceID: evidenceID,
		}
	}

	for _, rec := range records {
		recID := strings.TrimSpace(rec.ID)
		if recID == "" {
			continue
		}
		recNodeID := "record:" + recID
		nodeKind := "record"
		switch strings.ToLower(strings.TrimSpace(rec.Kind)) {
		case "decision", "finding", "memory_block":
			nodeKind = strings.ToLower(strings.TrimSpace(rec.Kind))
		}
		label := contextpack.RedactSecrets(strings.TrimSpace(rec.Text))
		if len(label) > 120 {
			label = label[:120] + "..."
		}
		if label == "" {
			label = recID
		}
		conf := rec.Confidence
		if conf <= 0 {
			conf = 0.5
		}
		upsertNode(GraphNode{
			ID:         recNodeID,
			Kind:       nodeKind,
			Label:      label,
			Confidence: conf,
			Verified:   rec.Verified,
			Metadata: map[string]any{
				"record_kind": rec.Kind,
				"project_id":  rec.ProjectID,
			},
		})

		author := firstNonEmpty(rec.From, rec.Agent, rec.Source)
		if author != "" {
			authorID := "agent:" + strings.ToLower(author)
			upsertNode(GraphNode{
				ID:         authorID,
				Kind:       "agent",
				Label:      strings.ToLower(author),
				Confidence: 1.0,
				Verified:   true,
			})
			upsertEdge(authorID, recNodeID, "AUTHORED", recID, 1.0)
		}

		if to := strings.TrimSpace(rec.To); to != "" {
			toID := "agent:" + strings.ToLower(to)
			upsertNode(GraphNode{
				ID:         toID,
				Kind:       "agent",
				Label:      strings.ToLower(to),
				Confidence: 1.0,
				Verified:   true,
			})
			upsertEdge(recNodeID, toID, "ADDRESSED_TO", recID, 1.0)
		}

		redactedText := contextpack.RedactSecrets(rec.Text)
		for _, match := range mentionPattern.FindAllStringSubmatch(redactedText, -1) {
			if len(match) < 2 {
				continue
			}
			mentioned := strings.ToLower(strings.TrimSpace(match[1]))
			if mentioned == "" {
				continue
			}
			agentID := "agent:" + mentioned
			upsertNode(GraphNode{
				ID:         agentID,
				Kind:       "agent",
				Label:      mentioned,
				Confidence: conf,
				Verified:   rec.Verified,
			})
			upsertEdge(recNodeID, agentID, "MENTIONS_AGENT", recID, 1.0)
		}

		for _, match := range filePathPattern.FindAllStringSubmatch(redactedText, -1) {
			if len(match) < 2 {
				continue
			}
			cleanPath := filepath.ToSlash(filepath.Clean(strings.TrimSpace(match[1])))
			if cleanPath == "." || cleanPath == "" || strings.HasPrefix(cleanPath, "..") {
				continue
			}
			fileID := "file:" + cleanPath
			upsertNode(GraphNode{
				ID:         fileID,
				Kind:       "file",
				Label:      cleanPath,
				Confidence: conf,
				Verified:   rec.Verified,
			})
			upsertEdge(recNodeID, fileID, "AFFECTS_FILE", recID, 1.0)
			if author != "" {
				upsertEdge("agent:"+strings.ToLower(author), fileID, "AFFECTS_FILE", recID, 0.5)
			}
		}

		for _, rawTag := range rec.Tags {
			tag := strings.ToLower(strings.TrimSpace(rawTag))
			if tag == "" {
				continue
			}
			topicID := "topic:" + tag
			upsertNode(GraphNode{
				ID:         topicID,
				Kind:       "topic",
				Label:      tag,
				Confidence: conf,
				Verified:   rec.Verified,
			})
			upsertEdge(recNodeID, topicID, "TAGGED_WITH", recID, 1.0)
		}

		if sup := strings.TrimSpace(rec.Supersedes); sup != "" {
			supID := "record:" + sup
			upsertNode(GraphNode{
				ID:         supID,
				Kind:       "record",
				Label:      sup,
				Confidence: 0.5,
			})
			upsertEdge(recNodeID, supID, "SUPERSEDES", recID, 1.0)
		}
	}

	nodes := make([]GraphNode, 0, len(nodesByID))
	for _, n := range nodesByID {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })

	edges := make([]GraphEdge, 0, len(edgesByKey))
	for _, e := range edgesByKey {
		edges = append(edges, e)
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Relation < edges[j].Relation
	})

	return buildGraphResult(nodes, edges)
}

// Subgraph extracts a bounded k-hop neighborhood around focusID (or returns the
// full graph if focusID is empty).
func Subgraph(g Graph, focusID string, maxHops int) Graph {
	focusID = strings.TrimSpace(focusID)
	if focusID == "" {
		return g
	}
	if maxHops <= 0 {
		maxHops = 1
	}
	if maxHops > 4 {
		maxHops = 4
	}
	visited := map[string]int{focusID: 0}
	frontier := []string{focusID}
	for hop := 0; hop < maxHops && len(frontier) > 0; hop++ {
		var next []string
		for _, curr := range frontier {
			for _, e := range g.Edges {
				if e.From == curr {
					if _, seen := visited[e.To]; !seen {
						visited[e.To] = hop + 1
						next = append(next, e.To)
					}
				} else if e.To == curr {
					if _, seen := visited[e.From]; !seen {
						visited[e.From] = hop + 1
						next = append(next, e.From)
					}
				}
			}
		}
		frontier = next
	}

	nodes := make([]GraphNode, 0)
	for _, n := range g.Nodes {
		if _, ok := visited[n.ID]; ok {
			nodes = append(nodes, n)
		}
	}
	edges := make([]GraphEdge, 0)
	for _, e := range g.Edges {
		_, okFrom := visited[e.From]
		_, okTo := visited[e.To]
		if okFrom && okTo {
			edges = append(edges, e)
		}
	}
	return buildGraphResult(nodes, edges)
}

func buildGraphResult(nodes []GraphNode, edges []GraphEdge) Graph {
	summary := map[string]int{
		"nodes":   len(nodes),
		"edges":   len(edges),
		"agents":  0,
		"files":   0,
		"topics":  0,
		"records": 0,
	}
	for _, n := range nodes {
		switch n.Kind {
		case "agent":
			summary["agents"]++
		case "file":
			summary["files"]++
		case "topic":
			summary["topics"]++
		default:
			summary["records"]++
		}
	}
	return Graph{
		Nodes:   nodes,
		Edges:   edges,
		Summary: summary,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
