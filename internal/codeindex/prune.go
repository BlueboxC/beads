package codeindex

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/steveyegge/beads/memoryops"
)

type PrunePlan struct {
	CurrentBytes    int      `json:"current_bytes"`
	ObsoleteBytes   int      `json:"obsolete_bytes"`
	ObsoleteKeys    []string `json:"obsolete_keys"`
	RetainedBlobs   int      `json:"retained_blobs"`
	HistoryRetained bool     `json:"dolt_history_retained"`
	Deleted         int      `json:"deleted"`
}

func PlanPrune(plane map[string]string) (PrunePlan, error) {
	plan := PrunePlan{ObsoleteKeys: []string{}, HistoryRetained: true}
	index, err := Load(plane)
	if err != nil || index.Version == 0 {
		return plan, errors.New("prune requires a complete readable current index")
	}
	var entry manifest
	if err := unpack(plane[manifestKey], &entry); err != nil {
		return plan, err
	}
	retained := make(map[string]bool)
	for _, part := range entry.Parts {
		key := Prefix + "blob/" + part
		retained[key] = true
		var refs []reference
		if err := unpack(plane[key], &refs); err != nil {
			return plan, err
		}
		entry.References = append(entry.References, refs...)
	}
	for _, ref := range entry.References {
		key := Prefix + "blob/" + ref.Blob
		retained[key] = true
		var file fileFragments
		if err := unpack(plane[key], &file); err != nil {
			return plan, err
		}
		for _, part := range file.Parts {
			retained[Prefix+"blob/"+part] = true
		}
	}
	plan.CurrentBytes = len(plane[manifestKey])
	for key, value := range plane {
		if !strings.HasPrefix(key, Prefix+"blob/") {
			continue
		}
		if strings.TrimPrefix(key, Prefix+"blob/") != digest([]byte(value)) || (!strings.HasPrefix(value, "code-v1:") && !strings.HasPrefix(value, fragmentPrefix)) {
			return plan, errors.New("unrecognized or corrupt derived blob; prune refused")
		}
		if retained[key] {
			plan.CurrentBytes += len(value)
			plan.RetainedBlobs++
		} else {
			plan.ObsoleteKeys = append(plan.ObsoleteKeys, key)
			plan.ObsoleteBytes += len(value)
		}
	}
	sort.Strings(plan.ObsoleteKeys)
	return plan, nil
}

// ApplyPrune guards the complete snapshot and delegates one atomic deletion to
// storage. Atomic Save republishes all needed blobs, including previously reused
// ones, so concurrent prune/publication cannot leave a broken manifest.
func ApplyPrune(ctx context.Context, memories memoryops.Memories, plane map[string]string) (PrunePlan, error) {
	plan, err := PlanPrune(plane)
	if err != nil {
		return plan, err
	}
	atomic, ok := memories.(memoryops.AtomicMemories)
	if !ok {
		return plan, errors.New("atomic memory batches unavailable; prune refused")
	}
	if len(plan.ObsoleteKeys) == 0 {
		return plan, nil
	}
	expected := map[string]string{manifestKey: plane[manifestKey]}
	for _, key := range plan.ObsoleteKeys {
		expected[key] = plane[key]
	}
	result, err := atomic.Apply(ctx, memoryops.BatchRequest{Expected: expected, Forget: plan.ObsoleteKeys})
	if err != nil {
		return plan, err
	}
	plan.Deleted = result.Deleted
	return plan, nil
}
