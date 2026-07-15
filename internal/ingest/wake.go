// Package ingest 实现持久化候选队列与 GitHub enrich Worker。
package ingest

// WakeSignal 是容量为 1 的边沿触发信号。
// 连续提交只需要保证 Worker 至少被唤醒一次，不能让 HTTP handler 因 channel 满而阻塞。
type WakeSignal struct {
	channel chan struct{}
}

func NewWakeSignal() *WakeSignal {
	return &WakeSignal{channel: make(chan struct{}, 1)}
}

// Notify 非阻塞发送唤醒信号；返回 false 表示已有未消费信号，而不是失败。
func (w *WakeSignal) Notify() bool {
	select {
	case w.channel <- struct{}{}:
		return true
	default:
		return false
	}
}

func (w *WakeSignal) C() <-chan struct{} { return w.channel }
