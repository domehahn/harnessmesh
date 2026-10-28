package assurance

import (
	"fmt"
	"sync"
)

// RuntimeGraph tracks declared capabilities separately from observed use. A
// newly observed capability never becomes authorized implicitly; it is marked
// as drift and must be admitted by policy.
type RuntimeGraph struct {
	mu       sync.RWMutex
	graph    Graph
	observed map[string]int
	drift    map[string]bool
}

func NewRuntimeGraph() *RuntimeGraph {
	return &RuntimeGraph{graph: Graph{Nodes: []Capability{}, Edges: map[string][]string{}}, observed: map[string]int{}, drift: map[string]bool{}}
}
func (g *RuntimeGraph) Declare(capability Capability, parents ...string) error {
	if capability.ID == "" {
		return fmt.Errorf("capability id is required")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, existing := range g.graph.Nodes {
		if existing.ID == capability.ID {
			return fmt.Errorf("capability %q already declared", capability.ID)
		}
	}
	g.graph.Nodes = append(g.graph.Nodes, capability)
	for _, parent := range parents {
		g.graph.Edges[parent] = append(g.graph.Edges[parent], capability.ID)
	}
	return nil
}
func (g *RuntimeGraph) Observe(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, node := range g.graph.Nodes {
		if node.ID == id {
			g.observed[id]++
			return node.Allowed
		}
	}
	g.observed[id]++
	g.drift[id] = true
	return false
}
func (g *RuntimeGraph) Snapshot() (Graph, map[string]int, map[string]bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	nodes := append([]Capability(nil), g.graph.Nodes...)
	edges := map[string][]string{}
	for k, values := range g.graph.Edges {
		edges[k] = append([]string(nil), values...)
	}
	counts := map[string]int{}
	for k, v := range g.observed {
		counts[k] = v
	}
	drift := map[string]bool{}
	for k, v := range g.drift {
		drift[k] = v
	}
	return Graph{Nodes: nodes, Edges: edges}, counts, drift
}
