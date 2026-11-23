package fuse

import (
	"testing"

	"litepan/internal/upload"
)

// 判定 staging 节点是否失效的规则必须只看“上传任务还有没有希望”，
// 不能看网盘列表：列表存在最终一致性，把正在上传的节点当成失效会误删用户数据。
func TestStagingNodeStale(t *testing.T) {
	cases := []struct {
		name    string
		hasTask bool
		status  string
		expired bool
		want    bool
	}{
		{name: "未入队且在保护期内", hasTask: false, expired: false, want: false},
		{name: "未入队且已超时", hasTask: false, expired: true, want: true},
		{name: "排队中", hasTask: true, status: upload.StatusPending, want: false},
		{name: "上传中", hasTask: true, status: upload.StatusRunning, want: false},
		{name: "暂停中", hasTask: true, status: upload.StatusPaused, want: false},
		{name: "已成功但列表还没追上", hasTask: true, status: upload.StatusSuccess, want: false},
		{name: "失败", hasTask: true, status: upload.StatusFailed, want: true},
		{name: "取消", hasTask: true, status: upload.StatusCanceled, want: true},
		{name: "跳过", hasTask: true, status: upload.StatusSkipped, want: true},
		{name: "任务失败但未超时也失效", hasTask: true, status: upload.StatusCanceled, expired: false, want: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stagingNodeStale(c.hasTask, c.status, c.expired); got != c.want {
				t.Fatalf("stagingNodeStale(%v, %q, %v)=%v，期望 %v", c.hasTask, c.status, c.expired, got, c.want)
			}
		})
	}
}
