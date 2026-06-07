package e

import "testing"

func TestBuildPendingKey(t *testing.T) {
	got := BuildPendingKey(123)
	want := "seckill:pending:123"
	if got != want {
		t.Fatalf("BuildPendingKey() = %s, want %s", got, want)
	}
}

func TestBuildProcessingAndCanceledKeys(t *testing.T) {
	orderNo := "ord_abc123def456"
	if got, want := BuildProcessingOrderKey(orderNo), "seckill:processing:"+orderNo; got != want {
		t.Fatalf("BuildProcessingOrderKey() = %s, want %s", got, want)
	}
	if got, want := BuildCanceledOrderKey(orderNo), "seckill:canceled:"+orderNo; got != want {
		t.Fatalf("BuildCanceledOrderKey() = %s, want %s", got, want)
	}
}

func TestBuildStockAndPurchasedKeys(t *testing.T) {
	if got, want := BuildStockKey(10, 20), "seckill:stock:10:20"; got != want {
		t.Fatalf("BuildStockKey() = %s, want %s", got, want)
	}
	if got, want := BuildPurchasedKey(10, 20), "seckill:purchased:10:20"; got != want {
		t.Fatalf("BuildPurchasedKey() = %s, want %s", got, want)
	}
}

func BenchmarkBuildPendingKey(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = BuildPendingKey(int64(i))
	}
}
