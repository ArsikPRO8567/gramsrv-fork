package rpc

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appsecret "telesrv/internal/app/secretchat"
	"telesrv/internal/domain"
	"telesrv/internal/postresponse"
	"telesrv/internal/store/memory"
)

func dispatchForReceivesUpdates(t *testing.T, sessions SessionBinder, wrapWithoutUpdates, loggedIn bool) context.Context {
	t.Helper()
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{Sessions: sessions}, zaptest.NewLogger(t), clock.System)

	var inner bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&inner); err != nil {
		t.Fatalf("encode help.getConfig: %v", err)
	}
	var in bin.Buffer
	if wrapWithoutUpdates {
		in.PutID(tg.InvokeWithoutUpdatesRequestTypeID)
	}
	in.Put(inner.Buf)

	ctx := postresponse.WithCallbacks(context.Background())
	if loggedIn {
		ctx = WithUserID(ctx, 1000000001)
	}
	if _, err := r.Dispatch(ctx, [8]byte{1}, 42, &in); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	return ctx
}

// TestDispatchMarksSessionReceivesUpdates 验证已登录连接发出的裸 RPC（未包
// invokeWithoutUpdates）即视为 updates 接收声明。仅靠 updates.getState/getDifference
// 置位会漏掉热恢复重连的客户端：它不重建同步基线，置位永不发生时主动推送一直
// 暂存直至超时丢弃，表现为另一端消息不再实时同步。
func TestDispatchMarksSessionReceivesUpdates(t *testing.T) {
	sessions := &captureSessions{}
	ctx := dispatchForReceivesUpdates(t, sessions, false, true)
	if sessions.snapshot().receives {
		t.Fatal("plain RPC marked receivesUpdates before rpc_result delivery")
	}
	postresponse.Run(ctx)
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if !sessions.receives {
		t.Fatal("plain RPC from logged-in session must mark receivesUpdates")
	}
	if sessions.sessionID != 42 {
		t.Fatalf("marked session_id = %d, want 42", sessions.sessionID)
	}
}

type updatesStateCaptureSessions struct {
	*captureSessions
}

func (s *updatesStateCaptureSessions) ReceivesUpdatesForAuthKey([8]byte, int64) bool {
	return s.snapshot().receives
}

func TestDispatchPushesCompleteSelfProfileOnceWhenSessionBecomesReady(t *testing.T) {
	const (
		userID    = int64(1000000311)
		sessionID = int64(311)
	)
	rawAuthKeyID := [8]byte{31}
	self := domain.User{
		ID:         userID,
		AccessHash: 3111,
		FirstName:  "Alice",
		Username:   "Alice",
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: userID}
	registry := newFakeUsernameRegistry()
	registry.byPeer[peer] = []domain.Username{
		{Username: "Alice", Active: true, Editable: true, SortOrder: 0},
		{Username: "aliceCollect0728b", Active: true, SortOrder: 1, CollectibleID: 2},
		{Username: "aliceCollect0728a", Active: true, SortOrder: 2, CollectibleID: 1},
	}
	sessions := &updatesStateCaptureSessions{captureSessions: &captureSessions{}}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Sessions:  sessions,
		Users:     staticUsersService{user: self},
		Usernames: registry,
	}, zaptest.NewLogger(t), clock.System)

	dispatch := func() context.Context {
		t.Helper()
		var in bin.Buffer
		if err := (&tg.HelpGetConfigRequest{}).Encode(&in); err != nil {
			t.Fatalf("encode help.getConfig: %v", err)
		}
		ctx := postresponse.WithCallbacks(WithUserID(context.Background(), userID))
		if _, err := r.Dispatch(ctx, rawAuthKeyID, sessionID, &in); err != nil {
			t.Fatalf("dispatch help.getConfig: %v", err)
		}
		return ctx
	}

	ctx := dispatch()
	if got := sessions.snapshot(); got.receives || got.sessionPushCalls != 0 {
		t.Fatalf("pre-delivery readiness = receives:%v pushes:%d, want false/0", got.receives, got.sessionPushCalls)
	}
	postresponse.Run(ctx)

	got := sessions.snapshot()
	if !got.receives || got.receivesCalls != 1 || got.sessionPushCalls != 1 {
		t.Fatalf("post-delivery readiness = receives:%v ready_calls:%d pushes:%d, want true/1/1",
			got.receives, got.receivesCalls, got.sessionPushCalls)
	}
	updates, ok := got.message.(*tg.Updates)
	if !ok || len(updates.Updates) != 2 || len(updates.Users) != 1 {
		t.Fatalf("self refresh = %T %+v, want two updates and one user", got.message, got.message)
	}
	nameRefresh, ok := updates.Updates[0].(*tg.UpdateUserName)
	if !ok || nameRefresh.UserID != userID || nameRefresh.FirstName != "Alice" || nameRefresh.LastName != "" {
		t.Fatalf("self name refresh = %T %+v, want updateUserName(%d, Alice)", updates.Updates[0], updates.Updates[0], userID)
	}
	wantUsernames := []string{"Alice", "aliceCollect0728b", "aliceCollect0728a"}
	if !reflect.DeepEqual(usernameStrings(nameRefresh.Usernames), wantUsernames) {
		t.Fatalf("self updateUserName usernames = %v, want %v", usernameStrings(nameRefresh.Usernames), wantUsernames)
	}
	refresh, ok := updates.Updates[1].(*tg.UpdateUser)
	if !ok || refresh.UserID != userID {
		t.Fatalf("self refresh update = %T %+v, want updateUser(%d)", updates.Updates[1], updates.Updates[1], userID)
	}
	projected, ok := updates.Users[0].(*tg.User)
	if !ok {
		t.Fatalf("self refresh user = %T, want *tg.User", updates.Users[0])
	}
	vector, set := projected.GetUsernames()
	if !set || !reflect.DeepEqual(usernameStrings(vector), wantUsernames) {
		t.Fatalf("self refresh usernames = %v (set %v), want %v", usernameStrings(vector), set, wantUsernames)
	}
	if scalar, set := projected.GetUsername(); !set || scalar != "Alice" {
		t.Fatalf("self refresh scalar username = %q (set %v), want Alice", scalar, set)
	}
	if updates.Seq != 0 {
		t.Fatalf("self refresh seq = %d, want 0", updates.Seq)
	}

	var wire bin.Buffer
	if err := tlprofile.EncodeObject(tlprofile.Profile228, updates, &wire); err != nil {
		t.Fatalf("encode Layer 228 self refresh: %v", err)
	}
	decoded, err := tlprofile.DecodeObject(tlprofile.Profile228, &bin.Buffer{Buf: wire.Copy()}, tlprofile.Limits{})
	if err != nil {
		t.Fatalf("decode Layer 228 self refresh: %v", err)
	}
	decodedUpdates, ok := decoded.(*tg.Updates)
	if !ok || len(decodedUpdates.Updates) != 2 || len(decodedUpdates.Users) != 1 {
		t.Fatalf("decoded Layer 228 self refresh = %T %+v", decoded, decoded)
	}
	decodedNameRefresh, ok := decodedUpdates.Updates[0].(*tg.UpdateUserName)
	if !ok || !reflect.DeepEqual(usernameStrings(decodedNameRefresh.Usernames), wantUsernames) {
		t.Fatalf("decoded Layer 228 updateUserName = %T usernames=%v, want %v",
			decodedUpdates.Updates[0], usernameStrings(decodedNameRefresh.Usernames), wantUsernames)
	}
	decodedUser := decodedUpdates.Users[0].(*tg.User)
	decodedVector, decodedSet := decodedUser.GetUsernames()
	if !decodedSet || !reflect.DeepEqual(usernameStrings(decodedVector), wantUsernames) {
		t.Fatalf("decoded Layer 228 usernames = %v (set %v), want %v",
			usernameStrings(decodedVector), decodedSet, wantUsernames)
	}

	// A session that is already fully ready must not receive the bootstrap again
	// on every ordinary RPC.
	postresponse.Run(dispatch())
	got = sessions.snapshot()
	if got.receivesCalls != 1 || got.sessionPushCalls != 1 || registry.peerCalls != 1 {
		t.Fatalf("repeat dispatch effects = ready_calls:%d pushes:%d registry_reads:%d, want 1/1/1",
			got.receivesCalls, got.sessionPushCalls, registry.peerCalls)
	}
}

func TestDispatchSuppressesSelfProfileWhenUsernameRegistryFails(t *testing.T) {
	const userID = int64(1000000312)
	registry := newFakeUsernameRegistry()
	registry.err = errors.New("registry unavailable")
	sessions := &updatesStateCaptureSessions{captureSessions: &captureSessions{}}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Sessions: sessions,
		Users: staticUsersService{user: domain.User{
			ID: userID, FirstName: "Alice", Username: "Alice",
		}},
		Usernames: registry,
	}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&in); err != nil {
		t.Fatalf("encode help.getConfig: %v", err)
	}
	ctx := postresponse.WithCallbacks(WithUserID(context.Background(), userID))
	if _, err := r.Dispatch(ctx, [8]byte{32}, 312, &in); err != nil {
		t.Fatalf("dispatch help.getConfig: %v", err)
	}
	postresponse.Run(ctx)
	got := sessions.snapshot()
	if !got.receives || got.sessionPushCalls != 0 {
		t.Fatalf("registry failure effects = receives:%v pushes:%d, want true/0", got.receives, got.sessionPushCalls)
	}
}

// TestDispatchSkipsReceivesUpdatesForInvokeWithoutUpdates 验证 invokeWithoutUpdates
// 包装的请求（media/temp 连接）不会把该 session 标记为 updates 接收者。
func TestDispatchSkipsReceivesUpdatesForInvokeWithoutUpdates(t *testing.T) {
	sessions := &captureSessions{}
	ctx := dispatchForReceivesUpdates(t, sessions, true, true)
	postresponse.Run(ctx)
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.receives {
		t.Fatal("invokeWithoutUpdates-wrapped RPC must not mark receivesUpdates")
	}
}

type captureBootstrapReadyStore struct {
	*memory.BootstrapUpdateJobStore
	readyCalls int
}

func (s *captureBootstrapReadyStore) MarkReadyForSession(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64) (int, error) {
	s.readyCalls++
	return s.BootstrapUpdateJobStore.MarkReadyForSession(ctx, userID, authKeyID, sessionID)
}

func TestInvokeWithoutUpdatesBaselineCommitsResultAndSecretEventsWithoutSubscribing(t *testing.T) {
	const userID int64 = 1000000201
	authKeyID := [8]byte{21}
	deviceKey := businessAuthKeyInt64(authKeyID)
	queue := memory.NewEncryptedQueueStore()
	secret := appsecret.NewService(memory.NewSecretChatStore(), queue, &seqSecretChatIDAllocator{})
	eventID, err := queue.AppendStateEvent(context.Background(), domain.EncryptedStateEvent{
		TargetUserID: userID,
		ChatID:       77,
		Type:         domain.EncryptedStateEventRead,
		MaxDate:      1700000200,
		Date:         1700000201,
	})
	if err != nil {
		t.Fatalf("append state event: %v", err)
	}
	sessions := &captureSessions{}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 4, Date: 1700000201}}
	bootstrap := &captureBootstrapReadyStore{BootstrapUpdateJobStore: memory.NewBootstrapUpdateJobStore()}
	r := New(Config{}, Deps{
		Sessions: sessions, Updates: updates, SecretChats: secret, BootstrapUpdates: bootstrap,
	}, zaptest.NewLogger(t), clock.System)

	var inner bin.Buffer
	if err := (&tg.UpdatesGetDifferenceRequest{Pts: 4, Date: 1700000201}).Encode(&inner); err != nil {
		t.Fatalf("encode getDifference: %v", err)
	}
	var wrapped bin.Buffer
	wrapped.PutID(tg.InvokeWithoutUpdatesRequestTypeID)
	wrapped.Put(inner.Raw())
	ctx := postresponse.WithCallbacks(WithAuthKeyID(WithSessionID(WithUserID(context.Background(), userID), 202), authKeyID))
	if _, err := r.Dispatch(ctx, authKeyID, 202, &wrapped); err != nil {
		t.Fatalf("dispatch wrapped baseline: %v", err)
	}
	if updates.commitCalls != 0 || sessions.snapshot().receivesCalls != 0 {
		t.Fatalf("pre-delivery effects = commits:%d ready_calls:%d", updates.commitCalls, sessions.snapshot().receivesCalls)
	}
	pending, err := queue.ListUndeliveredStateEvents(context.Background(), userID, deviceKey, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != eventID {
		t.Fatalf("pending before delivery = %+v err=%v", pending, err)
	}

	postresponse.Run(ctx)
	if updates.commitCalls != 1 || updates.committedState.Pts != 4 {
		t.Fatalf("delivered cursor commit = calls:%d state:%+v", updates.commitCalls, updates.committedState)
	}
	if got := sessions.snapshot(); got.receives || got.receivesCalls != 0 {
		t.Fatalf("invokeWithoutUpdates subscribed session: receives=%v calls=%d", got.receives, got.receivesCalls)
	}
	if bootstrap.readyCalls != 0 {
		t.Fatalf("invokeWithoutUpdates released bootstrap %d times", bootstrap.readyCalls)
	}
	pending, err = queue.ListUndeliveredStateEvents(context.Background(), userID, deviceKey, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("secret events after delivered wrapped baseline = %+v err=%v", pending, err)
	}
}

// TestDispatchSkipsReceivesUpdatesWhenLoggedOut 验证未登录连接的 RPC 不置位。
func TestDispatchSkipsReceivesUpdatesWhenLoggedOut(t *testing.T) {
	sessions := &captureSessions{}
	ctx := dispatchForReceivesUpdates(t, sessions, false, false)
	postresponse.Run(ctx)
	sessions.mu.Lock()
	defer sessions.mu.Unlock()
	if sessions.receives {
		t.Fatal("RPC without bound user must not mark receivesUpdates")
	}
}

type fifoFlushCaptureSessions struct {
	*captureSessions
	flushMu sync.Mutex
	pending []int
	flushed []int
}

func (s *fifoFlushCaptureSessions) SetReceivesUpdatesForAuthKey(rawAuthKeyID [8]byte, sessionID int64, receives bool) {
	if receives {
		s.flushMu.Lock()
		s.flushed = append(s.flushed, s.pending...)
		s.pending = nil
		s.flushMu.Unlock()
	}
	s.captureSessions.SetReceivesUpdatesForAuthKey(rawAuthKeyID, sessionID, receives)
}

func (s *fifoFlushCaptureSessions) flushedSnapshot() []int {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	return append([]int(nil), s.flushed...)
}

// TestDispatchDefersMembershipAndFIFOFlushUntilPostResponse pins the complete
// readiness barrier: channel membership and pending updates remain untouched
// while the rpc_result is only prepared, then the delivery hook rebuilds
// membership before SetReceivesUpdates drains the original FIFO order.
func TestDispatchDefersMembershipAndFIFOFlushUntilPostResponse(t *testing.T) {
	const (
		userID    = int64(1000000111)
		sessionID = int64(87)
	)
	channelSvc := appchannels.NewService(memory.NewChannelStore())
	created, err := channelSvc.CreateMegagroupFromCreateChat(context.Background(), userID, domain.CreateChannelRequest{
		Title: "delivery barrier",
		Date:  1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	sessions := &fifoFlushCaptureSessions{
		captureSessions: &captureSessions{},
		pending:         []int{11, 22, 33},
	}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Sessions: sessions,
		Channels: channelSvc,
	}, zaptest.NewLogger(t), clock.System)

	var in bin.Buffer
	if err := (&tg.HelpGetConfigRequest{}).Encode(&in); err != nil {
		t.Fatalf("encode help.getConfig: %v", err)
	}
	ctx := postresponse.WithCallbacks(WithUserID(context.Background(), userID))
	if _, err := r.Dispatch(ctx, [8]byte{7}, sessionID, &in); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := sessions.flushedSnapshot(); len(got) != 0 {
		t.Fatalf("pending flushed before result delivery: %v", got)
	}
	if got := sessions.onlineChannelMemberIDs(created.Channel.ID); len(got) != 0 {
		t.Fatalf("membership synced before result delivery: %v", got)
	}
	if sessions.snapshot().receives {
		t.Fatal("session ready before result delivery")
	}

	postresponse.Run(ctx)
	if got, want := sessions.flushedSnapshot(), []int{11, 22, 33}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FIFO flush after result delivery = %v, want %v", got, want)
	}
	if got := sessions.onlineChannelMemberIDs(created.Channel.ID); !reflect.DeepEqual(got, []int64{userID}) {
		t.Fatalf("membership after result delivery = %v, want [%d]", got, userID)
	}
	if !sessions.snapshot().receives {
		t.Fatal("session not ready after result delivery")
	}
}

type failingCurrentStateUpdates struct{ *captureUpdates }

func (s *failingCurrentStateUpdates) CurrentState(context.Context, int64) (domain.UpdateState, error) {
	return domain.UpdateState{}, errors.New("current state failed")
}

func TestFailedRPCDoesNotRegisterSessionReadyPostResponse(t *testing.T) {
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Sessions: sessions,
		Updates:  &failingCurrentStateUpdates{captureUpdates: &captureUpdates{}},
	}, zaptest.NewLogger(t), clock.System)
	var in bin.Buffer
	if err := (&tg.UpdatesGetStateRequest{}).Encode(&in); err != nil {
		t.Fatalf("encode updates.getState: %v", err)
	}
	ctx := postresponse.WithCallbacks(WithUserID(context.Background(), 1000000123))
	if _, err := r.Dispatch(ctx, [8]byte{8}, 91, &in); err == nil {
		t.Fatal("updates.getState unexpectedly succeeded")
	}
	postresponse.Run(ctx)
	if sessions.snapshot().receives {
		t.Fatal("failed RPC marked session ready")
	}
}
