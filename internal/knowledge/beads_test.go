package knowledge

import (
	"testing"
	"time"
)

func TestResolveBeadsDependencyGraphAndReadyQueue(t *testing.T) {
	now := time.Now().UTC()
	tasks := []BeadTask{
		{ID: "bd-1", Title: "Design schema", Status: BeadStatusDone, Priority: 1, UpdatedAt: now},
		{ID: "bd-2", Title: "Implement store", Status: BeadStatusOpen, Priority: 2, DependsOn: []string{"bd-1"}, UpdatedAt: now},
		{ID: "bd-3", Title: "Add API handler", Status: BeadStatusOpen, Priority: 1, DependsOn: []string{"bd-2"}, UpdatedAt: now},
		{ID: "bd-4", Title: "Write docs", Status: BeadStatusOpen, Priority: 1, UpdatedAt: now},
	}

	graph := ResolveBeads(tasks)
	if len(graph.Done) != 1 || graph.Done[0].ID != "bd-1" {
		t.Fatalf("expected bd-1 in Done, got %+v", graph.Done)
	}
	if len(graph.Ready) != 2 || graph.Ready[0].ID != "bd-4" || graph.Ready[1].ID != "bd-2" {
		t.Fatalf("expected ready queue [bd-4 (prio 1), bd-2 (prio 2)], got %+v", graph.Ready)
	}
	if len(graph.Blocked) != 1 || graph.Blocked[0].ID != "bd-3" {
		t.Fatalf("expected bd-3 blocked on bd-2, got %+v", graph.Blocked)
	}

	// Complete bd-2 in a later update; bd-3 becomes ready
	tasks = append(tasks, BeadTask{ID: "bd-2", Title: "Implement store", Status: BeadStatusDone, Priority: 2, DependsOn: []string{"bd-1"}, UpdatedAt: now.Add(time.Second)})
	graph2 := ResolveBeads(tasks)
	if len(graph2.Ready) != 2 || graph2.Ready[0].ID != "bd-3" {
		t.Fatalf("expected bd-3 to become ready after bd-2 completed, got %+v", graph2.Ready)
	}
}

func TestResolveBeadsDetectsCycles(t *testing.T) {
	tasks := []BeadTask{
		{ID: "cycle-a", Title: "Task A", Status: BeadStatusOpen, DependsOn: []string{"cycle-b"}},
		{ID: "cycle-b", Title: "Task B", Status: BeadStatusOpen, DependsOn: []string{"cycle-a"}},
	}
	graph := ResolveBeads(tasks)
	if len(graph.Ready) != 0 || len(graph.Cycles) != 2 {
		t.Fatalf("expected cyclic tasks to be blocked and flagged in Cycles, got %+v", graph)
	}
}
