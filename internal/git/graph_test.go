package git

import "testing"

func TestGraph(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		commits []LogCommit
	}{
		{"线性", []LogCommit{{SHA: "a", Parents: []string{"b"}}, {SHA: "b", Parents: []string{"c"}}}},
		{"合并", []LogCommit{{SHA: "a", Parents: []string{"b", "c"}}, {SHA: "b", Parents: []string{"d"}}, {SHA: "c", Parents: []string{"d"}}, {SHA: "d"}}},
		{"章鱼", []LogCommit{{SHA: "a", Parents: []string{"b", "c", "d"}}, {SHA: "b"}, {SHA: "c"}, {SHA: "d"}}},
		{"折叠", []LogCommit{{SHA: "a", Parents: []string{"b", "c", "d", "e", "f", "g", "h"}}, {SHA: "b"}, {SHA: "c"}, {SHA: "d"}, {SHA: "e"}, {SHA: "f"}, {SHA: "g"}, {SHA: "h"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := ComputeGraph(tc.commits, 6)
			if len(rows) != len(tc.commits) {
				t.Fatal(rows)
			}
			for _, row := range rows {
				if row.Lane > 5 {
					t.Fatal(row)
				}
				for _, e := range append(row.Upper, row.Lower...) {
					if e.From > 5 || e.To > 5 {
						t.Fatal(e)
					}
				}
			}
			if tc.name == "线性" && (rows[0].Color != rows[1].Color || len(rows[1].Lower) == 0) {
				t.Fatal(rows)
			}
		})
	}
}

func TestGraphEdges(t *testing.T) {
	t.Parallel()
	rows := ComputeGraph([]LogCommit{
		{SHA: "a", Parents: []string{"b", "c"}},
		{SHA: "b", Parents: []string{"d"}},
		{SHA: "c", Parents: []string{"d"}},
		{SHA: "d", Parents: []string{"outside"}},
	}, 6)
	has := func(edges []GraphEdge, want GraphEdge) bool {
		for _, edge := range edges {
			if edge == want {
				return true
			}
		}
		return false
	}
	if rows[0].Lane != 0 || rows[0].Color != 0 || !has(rows[0].Lower, GraphEdge{0, 0, 0}) || !has(rows[0].Lower, GraphEdge{0, 1, 1}) {
		t.Fatalf("分叉: %+v", rows[0])
	}
	if rows[1].Color != 0 || !has(rows[1].Lower, GraphEdge{1, 1, 1}) {
		t.Fatalf("穿行: %+v", rows[1])
	}
	if rows[2].Lane != 1 || rows[2].Color != 1 || !has(rows[2].Lower, GraphEdge{1, 0, 0}) || !has(rows[2].Lower, GraphEdge{0, 0, 0}) {
		t.Fatalf("合并: %+v", rows[2])
	}
	if rows[3].Color != 0 || !has(rows[3].Lower, GraphEdge{0, 0, 0}) {
		t.Fatalf("分页截断: %+v", rows[3])
	}
	octopus := ComputeGraph([]LogCommit{{SHA: "a", Parents: []string{"b", "c", "d"}}}, 6)
	if !has(octopus[0].Lower, GraphEdge{0, 2, 2}) {
		t.Fatalf("章鱼合并: %+v", octopus)
	}
	folded := ComputeGraph([]LogCommit{{SHA: "a", Parents: []string{"b", "c", "d", "e", "f", "g", "h"}}}, 6)
	if !has(folded[0].Lower, GraphEdge{0, 5, 0}) || !has(folded[0].Lower, GraphEdge{0, 5, 5}) {
		t.Fatalf("折叠: %+v", folded)
	}
}
