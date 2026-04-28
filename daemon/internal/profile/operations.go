package profile

import "fmt"

type RuntimeIntent struct {
	BackendKind     string
	FallbackPolicy  string
	ActiveProfileID string
}

func ImportNodes(current Document, nodes []Node) (Document, MergeStats) {
	return MergeNodes(current, nodes, false)
}

func SetActiveNode(current Document, nodeID string) (Document, error) {
	found := false
	for _, node := range current.Nodes {
		if node.ID == nodeID && !node.Stale {
			found = true
			break
		}
	}
	if !found {
		return Document{}, fmt.Errorf("active node is missing or stale")
	}
	current.ActiveNodeID = nodeID
	return current, nil
}

func ApplyRuntimeIntent(current Document, intent RuntimeIntent) Document {
	if intent.BackendKind != "" {
		current.Runtime.BackendKind = intent.BackendKind
	}
	if intent.FallbackPolicy != "" {
		current.Runtime.FallbackPolicy = intent.FallbackPolicy
	}
	if intent.ActiveProfileID != "" {
		current.ActiveNodeID = intent.ActiveProfileID
	}
	return current
}
