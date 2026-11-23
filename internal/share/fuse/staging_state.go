package fuse

import (
	"time"

	"litepan/internal/upload"
)

// stagingOrphanTTL 是一个 staging 节点"还没有拿到上传任务"或"任务已被移除"后
// 可以继续被当成有效文件的最长时间。超过它就当成孤儿回收，避免客户端异常退出
// （没走完 close、或上传任务被删）留下永久幽灵。
const stagingOrphanTTL = 10 * time.Minute

// stagingNodeStale 判断一个 staging 节点是否已经不可能变成网盘上的文件。
//
// 判据只看上传任务还有没有希望成功，不看网盘目录列表：列表存在最终一致性，
// 把"只是还看不见"的节点当成失效会误删用户刚写入的文件。因此只有两种情况算失效：
//   - 任务已经确定失败/取消/跳过（云端永远不会有这个文件）；
//   - 一直没有任务、或任务记录已消失，并且已经超过 stagingOrphanTTL。
//
// 排队中、上传中、暂停中、已成功的任务都算有效。
func stagingNodeStale(hasTask bool, status string, expired bool) bool {
	if !hasTask {
		return expired
	}
	switch status {
	case upload.StatusFailed, upload.StatusCanceled, upload.StatusSkipped:
		return true
	}
	return false
}
