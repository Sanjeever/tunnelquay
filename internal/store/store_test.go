package store

import (
	"testing"
	"time"
)

func TestHistoryRetentionFilteringAndRecovery(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour)
	recent := now.Add(-time.Hour)
	items := []Connection{{ID: "old", TunnelID: "deleted", DeviceID: "a", Protocol: "tcp", Source: "192.0.2.1:100", Started: old, Ended: &old}, {ID: "recent", TunnelID: "deleted", DeviceID: "b", Protocol: "udp", Source: "192.0.2.2:200", Started: recent, Ended: &recent}, {ID: "active", DeviceID: "a", Started: old}}
	if err = s.SaveConnections(items); err != nil {
		t.Fatal(err)
	}
	if err = s.Prune(now); err != nil {
		t.Fatal(err)
	}
	got, total, err := s.History(Filter{Page: 1, Limit: 1, DeviceID: "b", Protocol: "udp", Source: "192.0.2.2"})
	if err != nil || total != 1 || len(got) != 1 || got[0].ID != "recent" {
		t.Fatal(got, total, err)
	}
	_, total, err = s.History(Filter{Page: 1, Limit: 10, State: "current"})
	if err != nil || total != 1 {
		t.Fatal("进行中记录被清理", total, err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, total, err = s.History(Filter{Page: 1, Limit: 10})
	if err != nil || total != 2 {
		t.Fatal(total, err)
	}
	for _, c := range got {
		if c.ID == "active" && (c.Result != "interrupted" || c.Ended == nil) {
			t.Fatal("未标记异常中断")
		}
	}
}
