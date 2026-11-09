package api

import "testing"

func TestParseOfflineAccountIDs(t *testing.T) {
	cases := []struct {
		name    string
		note    string
		want    []int64
		wantErr bool
	}{
		{name: "空备注", note: "", want: nil},
		{name: "JSON 数组", note: `{"offline_account_ids":[2,8]}`, want: []int64{2, 8}},
		{name: "JSON 单值", note: `{"account_id":2}`, want: []int64{2}},
		{name: "键值写法", note: "account_id=2", want: []int64{2}},
		{name: "键值写法带其他字段", note: "备注 account_id=2, 用于 javdb", want: []int64{2}},
		{name: "非法 JSON", note: `{"offline_account_ids":`, wantErr: true},
		{name: "非法 ID", note: "account_id=abc", wantErr: true},
		{name: "没有绑定", note: "随便写的备注", want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOfflineAccountIDs(tc.note)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际拿到 %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("未预期的错误：%v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("账号数量不符：got %v want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("第 %d 个账号不符：got %v want %v", i, got, tc.want)
				}
			}
		})
	}
}

func TestScriptBearerToken(t *testing.T) {
	if _, err := scriptBearerToken(""); err == nil {
		t.Fatal("空头应当报错")
	}
	if _, err := scriptBearerToken("Basic abc"); err == nil {
		t.Fatal("非 Bearer 应当报错")
	}
	got, err := scriptBearerToken("Bearer abc123")
	if err != nil || got != "abc123" {
		t.Fatalf("解析错误：got %q err %v", got, err)
	}
}
