package mtprotoedge

import (
	"testing"

	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"
)

// TestReceivesUpdatesForAuthKeyRequiresMembershipSync 验证「完全就绪」查询同时要求
// receivesUpdates 与 channel membership 路由建立成功。membership 同步失败时该查询
// 必须返回 false，让按 RPC 置位的短路放行重试——否则该 session 会以「已置位但
// byMemberChannel 缺失」的状态静默漏收超级群推送，且 channel 维度没有 pts 缺口
// 信号可供客户端自愈。
func TestReceivesUpdatesForAuthKeyRequiresMembershipSync(t *testing.T) {
	sm := NewSessionManager(zaptest.NewLogger(t))
	raw := [8]byte{1, 2, 3}
	c := &Conn{sessionID: 42, authKeyID: raw}
	if err := c.FreezeLayerProfile(tlprofile.Profile227); err != nil {
		t.Fatal(err)
	}
	sm.Register(c)
	sm.BindUserForAuthKey(raw, 42, 100)

	sm.SetReceivesUpdatesForAuthKey(raw, 42, true)
	if sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("ready before membership sync — a failed sync would never be retried")
	}
	if sm.UpdatesActivationStartedForAuthKey(raw, 42) {
		t.Fatal("activation started before membership sync")
	}

	sm.SetSessionChannelMemberships(raw, 42, 100, []int64{7}, sm.ChannelMembershipGeneration(raw, 42))
	if !sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("not ready after successful membership sync")
	}
	if !sm.UpdatesActivationStartedForAuthKey(raw, 42) {
		t.Fatal("activation not started after successful membership sync")
	}

	// Pending FIFO drain counts as an activation already in progress even though
	// the session is not fully ready yet. A stale RPC callback must not enqueue a
	// duplicate cache-convergence update behind that same FIFO.
	key := sessionKey{authKeyID: raw, sessionID: 42}
	sm.mu.Lock()
	c.receivesUpdates.Store(false)
	sm.flushing[key] = true
	sm.mu.Unlock()
	if sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("flushing session reported fully ready")
	}
	if !sm.UpdatesActivationStartedForAuthKey(raw, 42) {
		t.Fatal("flushing session did not report activation started")
	}
	sm.mu.Lock()
	delete(sm.flushing, key)
	c.receivesUpdates.Store(true)
	sm.mu.Unlock()

	// 没有任何频道的账号：空列表的成功同步同样算就绪。
	sm.SetSessionChannelMemberships(raw, 42, 100, nil, sm.ChannelMembershipGeneration(raw, 42))
	if !sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("not ready after successful empty membership sync")
	}

	// userID 与连接当前绑定不一致（换号竞态）时不算就绪，等正确身份重试。
	sm.SetSessionChannelMemberships(raw, 42, 999, []int64{7}, sm.ChannelMembershipGeneration(raw, 42))
	if sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("ready after membership sync for mismatched user")
	}
	sm.SetSessionChannelMemberships(raw, 42, 100, []int64{7}, sm.ChannelMembershipGeneration(raw, 42))

	// 登出清除就绪标志。
	sm.BindUserForAuthKey(raw, 42, 0)
	if sm.ReceivesUpdatesForAuthKey(raw, 42) {
		t.Fatal("still ready after user unbind")
	}
}
