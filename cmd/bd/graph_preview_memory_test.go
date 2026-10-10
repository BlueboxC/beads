package main

import "testing"

func TestGraphPreviewMemoryMergePresence(t *testing.T) {
	for _, properties := range []map[string]any{
		nil, {}, {"title": ""}, {"body": "雪\n"},
		{"title": " Plan ", "body": "雪\n"},
	} {
		title, body, err := graphPreviewMemoryProperties(properties)
		if err != nil {
			t.Fatalf("refused valid merge %v: %v", properties, err)
		}
		if value, ok := properties["title"]; ok && (title == nil || *title != value) || !ok && title != nil {
			t.Fatalf("changed title presence: %v, %v", properties, title)
		}
		if value, ok := properties["body"]; ok && (body == nil || *body != value) || !ok && body != nil {
			t.Fatalf("changed body presence: %v, %v", properties, body)
		}
	}
	for _, properties := range []map[string]any{
		{"title": nil, "body": ""},
		{"title": "", "body": false}, {"title": "", "body": "", "metadata": map[string]any{}},
	} {
		if _, _, err := graphPreviewMemoryProperties(properties); err == nil {
			t.Fatalf("admitted invalid merge: %v", properties)
		}
	}
}
