package rpc

import (
	"bytes"
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
)

// Round52: канал-владелец открывает статистику, клиент не получает ответа.
// Тест гоняет полный wire-путь Dispatch для stats.getBroadcastStats.
func TestBroadcastStatsWireDispatch(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(41, "15550002101", "Owner")

	created, err := f.router.onChannelsCreateChannel(f.userCtx(owner), &tg.ChannelsCreateChannelRequest{
		Title:         "Test",
		About:         "test",
		Broadcast:     true,
		Megagroup:     false,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	updates, ok := created.(*tg.Updates)
	if !ok || len(updates.Chats) == 0 {
		t.Fatalf("create channel updates = %T", created)
	}
	channel, ok := updates.Chats[0].(*tg.Channel)
	if !ok {
		t.Fatalf("created chat = %T", updates.Chats[0])
	}

	// Wire request: stats.getBroadcastStats#ab42441a flags:0 channel:<inputChannel>
	var buf bin.Buffer
	buf.PutID(0xab42441a)
	buf.PutUint32(0) // flags: dark=false
	buf.PutID(tg.InputChannelTypeID)
	buf.PutLong(channel.ID)
	buf.PutLong(channel.AccessHash)

	req := bytes.Clone(buf.Buf)
	ctx := WithLayer(WithUserID(context.Background(), owner.ID), 227)
	enc, method, err := f.router.DispatchWithMethod(ctx, [8]byte{}, 1, &bin.Buffer{Buf: req})
	if err != nil {
		t.Fatalf("dispatch getBroadcastStats: %v (method=%s)", err, method)
	}
	if enc == nil {
		t.Fatal("dispatch result nil")
	}
	var out bin.Buffer
	if err := enc.Encode(&out); err != nil {
		t.Fatalf("encode result: %v", err)
	}
	t.Logf("result %s encoded %d bytes: %x", method, out.Len(), out.Buf[:min(64, out.Len())])
	stats, ok := enc.(*tg.StatsBroadcastStats)
	if !ok {
		t.Fatalf("result type = %T", enc)
	}
	if stats.GrowthGraph == nil {
		t.Fatal("growth graph nil")
	}
}
