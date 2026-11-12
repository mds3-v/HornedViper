package api

import "hornedviper/internal/store"

type swarmView struct {
	Run           store.SwarmRun
	Lanes         []store.SwarmLaneResult
	TotalAssets   int
	CoveredAssets int
	Uncovered     []int64
	FailedLanes   []string
}

func newSwarmView(run store.SwarmRun, lanes []store.SwarmLaneResult) swarmView {
	v := swarmView{Run: run, Lanes: lanes}
	if run.Mode != "shard" {
		return v
	}
	status := map[string]string{}
	for _, lane := range lanes {
		status[lane.LaneID] = lane.Status
	}
	for i, assets := range run.LaneAssets {
		v.TotalAssets += len(assets)
		laneID := laneIDForIndex(i)
		st := status[laneID]
		if st == "done" {
			v.CoveredAssets += len(assets)
			continue
		}
		v.Uncovered = append(v.Uncovered, assets...)
		v.FailedLanes = append(v.FailedLanes, laneID)
	}
	return v
}

func laneIDForIndex(i int) string {
	return "lane-" + itoa(i+1)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
