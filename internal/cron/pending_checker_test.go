package cron

import "testing"

func TestParseActivityIDFromKey(t *testing.T) {
	id, err := parseActivityIDFromKey("seckill:pending:456")
	if err != nil {
		t.Fatalf("parseActivityIDFromKey() error = %v", err)
	}
	if id != 456 {
		t.Fatalf("parseActivityIDFromKey() = %d, want 456", id)
	}
}

func TestParseActivityIDFromKeyInvalid(t *testing.T) {
	_, err := parseActivityIDFromKey("seckill:pending:")
	if err == nil {
		t.Fatalf("parseActivityIDFromKey() expected error")
	}
}

func BenchmarkParseActivityIDFromKey(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = parseActivityIDFromKey("seckill:pending:123")
	}
}
