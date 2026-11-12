package git

type GraphEdge struct {
	From, To int
	Color    int
}
type GraphRow struct {
	Lane, Color  int
	Upper, Lower []GraphEdge
}
type graphLane struct {
	sha   string
	color int
}

func ComputeGraph(commits []LogCommit, maxLanes int) []GraphRow {
	if maxLanes <= 0 {
		maxLanes = 6
	}
	lanes := []graphLane{}
	nextColor := 0
	rows := make([]GraphRow, 0, len(commits))
	capLane := func(i int) int {
		if i >= maxLanes-1 {
			return maxLanes - 1
		}
		return i
	}
	allocate := func(sha string) int {
		for i := range lanes {
			if lanes[i].sha == "" {
				lanes[i] = graphLane{sha, nextColor % 6}
				nextColor++
				return i
			}
		}
		lanes = append(lanes, graphLane{sha, nextColor % 6})
		nextColor++
		return len(lanes) - 1
	}
	for _, commit := range commits {
		lane := -1
		for i, l := range lanes {
			if l.sha == commit.SHA {
				lane = i
				break
			}
		}
		newLane := lane < 0
		if newLane {
			lane = allocate(commit.SHA)
		}
		row := GraphRow{Lane: capLane(lane), Color: lanes[lane].color}
		for i, l := range lanes {
			if l.sha != "" && !(newLane && i == lane) {
				row.Upper = append(row.Upper, GraphEdge{capLane(i), capLane(i), l.color})
			}
		}
		color := lanes[lane].color
		lanes[lane].sha = ""
		for n, parent := range commit.Parents {
			target := -1
			for i, l := range lanes {
				if l.sha == parent {
					target = i
					break
				}
			}
			if target < 0 {
				if n == 0 {
					target = lane
					lanes[target] = graphLane{parent, color}
				} else {
					target = allocate(parent)
				}
			}
			row.Lower = append(row.Lower, GraphEdge{capLane(lane), capLane(target), lanes[target].color})
		}
		for i, l := range lanes {
			if l.sha != "" && i != lane {
				row.Lower = append(row.Lower, GraphEdge{capLane(i), capLane(i), l.color})
			}
		}
		rows = append(rows, row)
	}
	return rows
}
