package halalcloud

import (
	"encoding/json"
	"testing"
)

func TestAdditionTimeoutFlexible(t *testing.T) {
	for _, raw := range []string{`{"timeout_seconds":"60"}`, `{"timeout_seconds":60}`, `{"timeout_seconds":60.0}`, `{"timeout_seconds":""}`} {
		var a Addition
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			t.Fatalf("%s 解析失败: %v", raw, err)
		}
		t.Logf("%s -> %d", raw, a.Timeout.Int())
	}
}
