package mtprotoedge

import "testing"

func TestRuntimeSnapshotIsNilSafeAndReportsConfiguredLimits(t *testing.T) {
	if got := (*Server)(nil).RuntimeSnapshot(); got != (RuntimeSnapshot{}) {
		t.Fatalf("nil server snapshot = %#v, want zero", got)
	}
	if got := (&Server{}).RuntimeSnapshot(); got != (RuntimeSnapshot{}) {
		t.Fatalf("partial server snapshot = %#v, want zero", got)
	}

	server := New(Options{})
	snapshot := server.RuntimeSnapshot()
	if snapshot.RawConnectionLimit <= 0 || snapshot.HandshakeLimit <= 0 {
		t.Fatalf("admission limits not reported: %#v", snapshot)
	}
	if snapshot.InboundRPCMaxTasks <= 0 || snapshot.InboundRPCMaxBytes <= 0 {
		t.Fatalf("inbound RPC limits not reported: %#v", snapshot)
	}
	if snapshot.InboundFrameMaxBytes <= 0 || snapshot.OutboundTrackedMaxBytes <= 0 || snapshot.OutboundWriteMaxBytes <= 0 {
		t.Fatalf("byte limits not reported: %#v", snapshot)
	}
	if snapshot.RawConnections != 0 || snapshot.ActiveSessions != 0 || snapshot.LogicalOutboxBytes != 0 {
		t.Fatalf("fresh server reported live ownership: %#v", snapshot)
	}
}
