// keyslot_test.go 子密钥并发名额的取/还对称性。
//
// 线上形态：面板把某把密钥的「单密钥并发上限」热改成 0（= 不限并发）时，若正好有
// 在途请求，旧实现按**归还时**读到的上限（0）直接 return → keyInFlight 永不归零 →
// 上限调回 N 后该密钥所有请求都 429 key_concurrency_limit，必须重启进程才恢复。
package server

import "testing"

func TestKeySlotReleasedWhenLimitHotChangedToZero(t *testing.T) {
	h := &Handler{keyInFlight: map[string]int{}}

	// 取号时上限 = 1 → 占一个名额
	if !h.acquireKeySlot("k1", 1) {
		t.Fatal("acquire with limit=1 should succeed")
	}
	if got := h.keyInFlight["k1"]; got != 1 {
		t.Fatalf("in-flight = %d, want 1", got)
	}
	// 上限被热改成 0 之后请求结束：名额必须归还（held 取的是取号时的判定）
	h.releaseKeySlot("k1", true)
	if got := h.keyInFlight["k1"]; got != 0 {
		t.Fatalf("slot leaked after hot-change to 0: in-flight = %d, want 0", got)
	}

	// 取号时上限就是 0（不限并发）→ 不占名额，归还也不该把计数减成负数
	if !h.acquireKeySlot("k2", 0) {
		t.Fatal("acquire with limit=0 (unlimited) should succeed")
	}
	h.releaseKeySlot("k2", false)
	if got := h.keyInFlight["k2"]; got != 0 {
		t.Fatalf("unlimited key counted a slot: in-flight = %d, want 0", got)
	}

	// 超额取号必须被挡住（上限 1 时第二个请求 429）
	if !h.acquireKeySlot("k3", 1) {
		t.Fatal("first acquire with limit=1 should succeed")
	}
	if h.acquireKeySlot("k3", 1) {
		t.Fatal("second acquire with limit=1 must be rejected")
	}
	h.releaseKeySlot("k3", true)
}
