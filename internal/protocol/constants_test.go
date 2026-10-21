package protocol

import (
	"testing"
	"time"
)

// TestConstantsMatchSpec 核对常量与 SPEC 4.6/4.9 数值一致，防止实现时抄错。
func TestConstantsMatchSpec(t *testing.T) {
	if MaxMessageBytes != 1048576 {
		t.Errorf("MaxMessageBytes=%d, 期望 1048576（1MiB）", MaxMessageBytes)
	}
	if SendQueueLimit != 4<<20 {
		t.Errorf("SendQueueLimit=%d, 期望 4MiB", SendQueueLimit)
	}
	if ControlQueueLimit != 64<<10 {
		t.Errorf("ControlQueueLimit=%d, 期望 64KiB", ControlQueueLimit)
	}
	if MailboxQuota != 100 {
		t.Errorf("MailboxQuota=%d, 期望 100", MailboxQuota)
	}
	if MailboxByteBudget != 8<<20 {
		t.Errorf("MailboxByteBudget=%d, 期望 8MiB", MailboxByteBudget)
	}
	if StorageBudget != 256<<20 {
		t.Errorf("StorageBudget=%d, 期望 256MiB", StorageBudget)
	}
	if DeliveryWindow != 8 {
		t.Errorf("DeliveryWindow=%d, 期望 8", DeliveryWindow)
	}
	if DefaultConcurrency != 2 {
		t.Errorf("DefaultConcurrency=%d, 期望 2", DefaultConcurrency)
	}
	if TailLimit != 64<<10 {
		t.Errorf("TailLimit=%d, 期望 64KiB", TailLimit)
	}
	if FinalAnswerLimit != 512<<10 {
		t.Errorf("FinalAnswerLimit=%d, 期望 512KiB", FinalAnswerLimit)
	}
	if ArtifactLimit != 640<<10 {
		t.Errorf("ArtifactLimit=%d, 期望 640KiB", ArtifactLimit)
	}
	if MaxArtifacts != 8 {
		t.Errorf("MaxArtifacts=%d, 期望 8", MaxArtifacts)
	}

	if RegisterTimeout != 5*time.Second {
		t.Errorf("RegisterTimeout=%v, 期望 5s", RegisterTimeout)
	}
	if PingInterval != 15*time.Second {
		t.Errorf("PingInterval=%v, 期望 15s", PingInterval)
	}
	if PongTimeout != 45*time.Second {
		t.Errorf("PongTimeout=%v, 期望 45s", PongTimeout)
	}
	if ReadWriteTimeout != 60*time.Second {
		t.Errorf("ReadWriteTimeout=%v, 期望 60s", ReadWriteTimeout)
	}
	if BackoffInitial != 500*time.Millisecond {
		t.Errorf("BackoffInitial=%v, 期望 0.5s", BackoffInitial)
	}
	if BackoffMax != 60*time.Second {
		t.Errorf("BackoffMax=%v, 期望 60s", BackoffMax)
	}
	if BackoffJitter != 0.2 {
		t.Errorf("BackoffJitter=%v, 期望 0.2（±20%%）", BackoffJitter)
	}
	if OfferAckTimeout != 30*time.Second {
		t.Errorf("OfferAckTimeout=%v, 期望 30s", OfferAckTimeout)
	}
	if SendStallTimeout != 60*time.Second {
		t.Errorf("SendStallTimeout=%v, 期望 60s", SendStallTimeout)
	}
	if TaskRetention != 7*24*time.Hour {
		t.Errorf("TaskRetention=%v, 期望 7d", TaskRetention)
	}
	if TerminalRetention != 24*time.Hour {
		t.Errorf("TerminalRetention=%v, 期望 24h", TerminalRetention)
	}
	if WorkdirQueueWait != 30*time.Minute {
		t.Errorf("WorkdirQueueWait=%v, 期望 30min", WorkdirQueueWait)
	}
}
