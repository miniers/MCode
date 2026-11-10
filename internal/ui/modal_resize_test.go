package mcodeui

import "testing"

func TestModalResizeApply(t *testing.T) {
	tests := []struct {
		name, edge string
		dx, dy     float32
		w, h, x, y float32
	}{
		{"right", "right", 40, 0, 540, 400, 20, 0},
		{"left", "left", 40, 0, 460, 400, 20, 0},
		{"maximum", "right", 200, 0, 600, 400, 50, 0},
		{"minimum", "left", 300, 0, 360, 400, 70, 0},
		{"corner", "top-left", -40, 30, 540, 370, -20, 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, h, x, y := modalResizeApply(tt.edge, tt.dx, tt.dy, 500, 400, 0, 0, 360, 240, 600, 700)
			if w != tt.w || h != tt.h || x != tt.x || y != tt.y {
				t.Fatalf("got (%v, %v, %v, %v), want (%v, %v, %v, %v)", w, h, x, y, tt.w, tt.h, tt.x, tt.y)
			}
		})
	}
}

func TestModalResizeSize(t *testing.T) {
	const key = "test-modal-resize-size"
	modalDragReset(key)
	t.Cleanup(func() { modalDragReset(key) })
	w, h := modalResizeSize(key, 520, 640, 360, 240, 1200, 1000)
	if w != 520 || h != 640 {
		t.Fatalf("default = %v, %v", w, h)
	}
	w, h = modalResizeSize(key, 720, 640, 360, 240, 600, 500)
	if w != 560 || h != 460 {
		t.Fatalf("clamped = %v, %v", w, h)
	}
}
