// Copyright 2015 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License. See the AUTHORS file
// for names of contributors.
//
// Author: Matt Tracy (matt.r.tracy@gmail.com)

package status

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/cockroachdb/cockroach/proto"
	"github.com/cockroachdb/cockroach/storage"
	"github.com/cockroachdb/cockroach/storage/engine"
	"github.com/cockroachdb/cockroach/util/hlc"
	"github.com/cockroachdb/cockroach/util/leaktest"
)

type byTimeAndName []proto.TimeSeriesData

func (a byTimeAndName) Len() int      { return len(a) }
func (a byTimeAndName) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a byTimeAndName) Less(i, j int) bool {
	if a[i].Name != a[j].Name {
		return a[i].Name < a[j].Name
	}
	return a[i].Datapoints[0].TimestampNanos < a[j].Datapoints[0].TimestampNanos
}

type byStoreID []proto.StoreID

func (a byStoreID) Len() int      { return len(a) }
func (a byStoreID) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a byStoreID) Less(i, j int) bool {
	return a[i] < a[j]
}

type byStoreDescID []*storage.StoreStatus

func (a byStoreDescID) Len() int      { return len(a) }
func (a byStoreDescID) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a byStoreDescID) Less(i, j int) bool {
	return a[i].Desc.StoreID < a[j].Desc.StoreID
}

// TestNodeStatusRecorder verifies that the time series data generated by a
// recorder matches the data added to the monitor.
func TestNodeStatusRecorder(t *testing.T) {
	defer leaktest.AfterTest(t)
	nodeDesc := proto.NodeDescriptor{
		NodeID: proto.NodeID(1),
	}
	storeDesc1 := proto.StoreDescriptor{
		StoreID: proto.StoreID(1),
		Capacity: proto.StoreCapacity{
			Capacity:  100,
			Available: 50,
		},
	}
	storeDesc2 := proto.StoreDescriptor{
		StoreID: proto.StoreID(2),
		Capacity: proto.StoreCapacity{
			Capacity:  200,
			Available: 75,
		},
	}
	desc1 := &proto.RangeDescriptor{
		RaftID:   1,
		StartKey: proto.Key("a"),
		EndKey:   proto.Key("b"),
	}
	desc2 := &proto.RangeDescriptor{
		RaftID:   2,
		StartKey: proto.Key("b"),
		EndKey:   proto.Key("c"),
	}
	stats := engine.MVCCStats{
		LiveBytes:       1,
		KeyBytes:        2,
		ValBytes:        3,
		IntentBytes:     4,
		LiveCount:       5,
		KeyCount:        6,
		ValCount:        7,
		IntentCount:     8,
		IntentAge:       9,
		GCBytesAge:      10,
		LastUpdateNanos: 1 * 1E9,
	}

	// Create a monitor and a recorder which uses the monitor.
	monitor := NewNodeStatusMonitor()
	manual := hlc.NewManualClock(100)
	recorder := NewNodeStatusRecorder(monitor, hlc.NewClock(manual.UnixNano))

	// Initialization events.
	monitor.OnStartNode(&StartNodeEvent{
		Desc:      nodeDesc,
		StartedAt: 50,
	})
	monitor.OnStartStore(&storage.StartStoreEvent{
		StoreID:   proto.StoreID(1),
		StartedAt: 60,
	})
	monitor.OnStartStore(&storage.StartStoreEvent{
		StoreID:   proto.StoreID(2),
		StartedAt: 70,
	})
	monitor.OnStoreStatus(&storage.StoreStatusEvent{
		Desc: &storeDesc1,
	})
	monitor.OnStoreStatus(&storage.StoreStatusEvent{
		Desc: &storeDesc2,
	})

	// Add some data to the monitor by simulating incoming events.
	monitor.OnBeginScanRanges(&storage.BeginScanRangesEvent{
		StoreID: proto.StoreID(1),
	})
	monitor.OnBeginScanRanges(&storage.BeginScanRangesEvent{
		StoreID: proto.StoreID(2),
	})
	monitor.OnRegisterRange(&storage.RegisterRangeEvent{
		StoreID: proto.StoreID(1),
		Desc:    desc1,
		Stats:   stats,
		Scan:    true,
	})
	monitor.OnRegisterRange(&storage.RegisterRangeEvent{
		StoreID: proto.StoreID(1),
		Desc:    desc2,
		Stats:   stats,
		Scan:    true,
	})
	monitor.OnRegisterRange(&storage.RegisterRangeEvent{
		StoreID: proto.StoreID(2),
		Desc:    desc1,
		Stats:   stats,
		Scan:    true,
	})
	monitor.OnEndScanRanges(&storage.EndScanRangesEvent{
		StoreID: proto.StoreID(1),
	})
	monitor.OnEndScanRanges(&storage.EndScanRangesEvent{
		StoreID: proto.StoreID(2),
	})
	monitor.OnUpdateRange(&storage.UpdateRangeEvent{
		StoreID: proto.StoreID(1),
		Desc:    desc1,
		Delta:   stats,
	})
	// Periodically published events.
	monitor.OnReplicationStatus(&storage.ReplicationStatusEvent{
		StoreID:              proto.StoreID(1),
		LeaderRangeCount:     1,
		AvailableRangeCount:  2,
		ReplicatedRangeCount: 0,
	})
	monitor.OnReplicationStatus(&storage.ReplicationStatusEvent{
		StoreID:              proto.StoreID(2),
		LeaderRangeCount:     1,
		AvailableRangeCount:  2,
		ReplicatedRangeCount: 0,
	})
	// Node Events.
	monitor.OnCallSuccess(&CallSuccessEvent{
		NodeID: proto.NodeID(1),
		Method: proto.Get,
	})
	monitor.OnCallSuccess(&CallSuccessEvent{
		NodeID: proto.NodeID(1),
		Method: proto.Put,
	})
	monitor.OnCallError(&CallErrorEvent{
		NodeID: proto.NodeID(1),
		Method: proto.Scan,
	})

	generateNodeData := func(nodeId int, name string, time, val int64) proto.TimeSeriesData {
		return proto.TimeSeriesData{
			Name: fmt.Sprintf(nodeTimeSeriesNameFmt, name, proto.StoreID(nodeId)),
			Datapoints: []*proto.TimeSeriesDatapoint{
				{
					TimestampNanos: time,
					Value:          float64(val),
				},
			},
		}
	}

	generateStoreData := func(storeId int, name string, time, val int64) proto.TimeSeriesData {
		return proto.TimeSeriesData{
			Name: fmt.Sprintf(storeTimeSeriesNameFmt, name, proto.StoreID(storeId)),
			Datapoints: []*proto.TimeSeriesDatapoint{
				{
					TimestampNanos: time,
					Value:          float64(val),
				},
			},
		}
	}

	// Generate the expected return value of recorder.GetTimeSeriesData(). This
	// data was manually generated, but is based on a simple multiple of the
	// "stats" collection above.
	expected := []proto.TimeSeriesData{
		// Store 1 should have accumulated 3x stats from two ranges.
		generateStoreData(1, "livebytes", 100, 3),
		generateStoreData(1, "keybytes", 100, 6),
		generateStoreData(1, "valbytes", 100, 9),
		generateStoreData(1, "intentbytes", 100, 12),
		generateStoreData(1, "livecount", 100, 15),
		generateStoreData(1, "keycount", 100, 18),
		generateStoreData(1, "valcount", 100, 21),
		generateStoreData(1, "intentcount", 100, 24),
		generateStoreData(1, "intentage", 100, 27),
		generateStoreData(1, "gcbytesage", 100, 30),
		generateStoreData(1, "lastupdatenanos", 100, 3*1e9),
		generateStoreData(1, "ranges", 100, 2),
		generateStoreData(1, "ranges.leader", 100, 1),
		generateStoreData(1, "ranges.available", 100, 2),
		generateStoreData(1, "ranges.replicated", 100, 0),
		generateStoreData(1, "capacity", 100, 100),
		generateStoreData(1, "capacity.available", 100, 50),

		// Store 2 should have accumulated 1 copy of stats
		generateStoreData(2, "livebytes", 100, 1),
		generateStoreData(2, "keybytes", 100, 2),
		generateStoreData(2, "valbytes", 100, 3),
		generateStoreData(2, "intentbytes", 100, 4),
		generateStoreData(2, "livecount", 100, 5),
		generateStoreData(2, "keycount", 100, 6),
		generateStoreData(2, "valcount", 100, 7),
		generateStoreData(2, "intentcount", 100, 8),
		generateStoreData(2, "intentage", 100, 9),
		generateStoreData(2, "gcbytesage", 100, 10),
		generateStoreData(2, "lastupdatenanos", 100, 1*1e9),
		generateStoreData(2, "ranges", 100, 1),
		generateStoreData(2, "ranges.leader", 100, 1),
		generateStoreData(2, "ranges.available", 100, 2),
		generateStoreData(2, "ranges.replicated", 100, 0),
		generateStoreData(2, "capacity", 100, 200),
		generateStoreData(2, "capacity.available", 100, 75),

		// Node stats.
		generateNodeData(1, "calls.success", 100, 2),
		generateNodeData(1, "calls.error", 100, 1),
	}

	actual := recorder.GetTimeSeriesData()
	sort.Sort(byTimeAndName(actual))
	sort.Sort(byTimeAndName(expected))
	if a, e := actual, expected; !reflect.DeepEqual(a, e) {
		t.Errorf("recorder did not yield expected time series collection; expected %v, got %v", e, a)
	}

	expectedNodeSummary := &NodeStatus{
		Desc:      nodeDesc,
		StartedAt: 50,
		UpdatedAt: 100,
		StoreIDs: []proto.StoreID{
			proto.StoreID(1),
			proto.StoreID(2),
		},
		RangeCount:           3,
		LeaderRangeCount:     2,
		AvailableRangeCount:  4,
		ReplicatedRangeCount: 0,
	}
	expectedStoreSummaries := []*storage.StoreStatus{
		{
			Desc:                 storeDesc1,
			NodeID:               proto.NodeID(1),
			UpdatedAt:            100,
			StartedAt:            60,
			RangeCount:           2,
			LeaderRangeCount:     1,
			AvailableRangeCount:  2,
			ReplicatedRangeCount: 0,
		},
		{
			Desc:                 storeDesc2,
			NodeID:               proto.NodeID(1),
			StartedAt:            70,
			UpdatedAt:            100,
			RangeCount:           1,
			LeaderRangeCount:     1,
			AvailableRangeCount:  2,
			ReplicatedRangeCount: 0,
		},
	}
	// Use base stats to generate expected summary stat values.
	for i := 0; i < 3; i++ {
		expectedStoreSummaries[0].Stats.Add(&stats)
	}
	expectedStoreSummaries[1].Stats.Add(&stats)
	for _, ss := range expectedStoreSummaries {
		expectedNodeSummary.Stats.Add(&ss.Stats)
	}

	nodeSummary, storeSummaries := recorder.GetStatusSummaries()
	sort.Sort(byStoreDescID(storeSummaries))
	sort.Sort(byStoreID(nodeSummary.StoreIDs))
	if a, e := nodeSummary, expectedNodeSummary; !reflect.DeepEqual(a, e) {
		t.Errorf("recorder did not produce expected NodeSummary; expected %v, got %v", e, a)
	}
	if a, e := storeSummaries, expectedStoreSummaries; !reflect.DeepEqual(a, e) {
		t.Errorf("recorder did not produce expected StoreSummaries; expected %v, got %v", e, a)
	}
}
