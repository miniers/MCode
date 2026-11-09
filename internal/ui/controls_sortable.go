package mcodeui

import "github.com/egoist/mygo/ui"

type sortablePayload struct {
	Scope, ID string
}

func SortableSource(handle ui.Element, scope, id string) {
	handle.Drag(sortablePayload{Scope: scope, ID: id})
}

func SortableTarget(row ui.Element, scope, targetID string) (overID, droppedID string) {
	if payload, ok := ui.DragOver[sortablePayload](row); ok {
		overID, _ = sortableAccept(payload, scope, targetID)
	}
	if payload, ok := ui.Drop[sortablePayload](row); ok {
		droppedID, _ = sortableAccept(payload, scope, targetID)
	}
	return
}

func sortableAccept(p sortablePayload, scope, targetID string) (string, bool) {
	if p.Scope != scope || p.ID == "" || (targetID != "" && p.ID == targetID) {
		return "", false
	}
	return p.ID, true
}
