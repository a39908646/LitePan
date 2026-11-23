//go:build fuse

package fuse

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"litepan/internal/domain"
	"litepan/internal/upload"
)

type recordingUploadManager struct {
	tempDir string
	params  []upload.CreateParams
	renamed []string
	deleted []string
	// tasks 用于模拟上传任务状态；nil 表示任务记录已不存在。
	tasks map[string]*upload.Task
	// createErr 非空时 Create 直接失败，用来模拟“入队失败”。
	createErr error
}

func (m *recordingUploadManager) Create(_ context.Context, params upload.CreateParams) (*upload.Task, error) {
	m.params = append(m.params, params)
	if m.createErr != nil {
		return nil, m.createErr
	}
	return &upload.Task{TaskID: "empty-upload"}, nil
}

func (m *recordingUploadManager) Get(_ context.Context, taskID string) (*upload.Task, bool) {
	if m.tasks == nil {
		return nil, false
	}
	task, ok := m.tasks[taskID]
	return task, ok
}

func (m *recordingUploadManager) TempDir() string { return m.tempDir }

func (m *recordingUploadManager) TempRegistry() *upload.TempRegistry { return nil }

func (m *recordingUploadManager) RenameTask(_ context.Context, taskID, newName, _ string, _ string) (bool, error) {
	m.renamed = append(m.renamed, taskID+":"+newName)
	return true, nil
}

func (m *recordingUploadManager) Delete(_ context.Context, taskID string, _ bool) (bool, error) {
	m.deleted = append(m.deleted, taskID)
	return true, nil
}

func TestStagingUploadFlushQueuesEmptyFile(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "empty-*")
	if err != nil {
		t.Fatalf("创建空文件失败: %v", err)
	}
	manager := &recordingUploadManager{tempDir: t.TempDir()}
	handle := &stagingUploadHandle{
		node:      &stagingFile{},
		uploads:   manager,
		accountID: 1,
		parentID:  "root",
		fileName:  "empty.txt",
		tempPath:  tmp.Name(),
		file:      tmp,
		flags:     syscall.O_WRONLY,
	}
	t.Cleanup(func() { _ = handle.Release(context.Background()) })

	if errno := handle.Flush(context.Background()); errno != 0 {
		t.Fatalf("空文件 Flush 不应失败，errno=%v", errno)
	}
	if len(manager.params) != 1 {
		t.Fatalf("期望创建一个上传任务，实际 %d", len(manager.params))
	}
	if manager.params[0].TotalBytes != 0 {
		t.Fatalf("空文件任务大小应为 0，实际 %d", manager.params[0].TotalBytes)
	}
}

func TestStagingRenameUpdatesNodeAndTask(t *testing.T) {
	manager := &recordingUploadManager{tempDir: t.TempDir()}
	mount := &domain.FuseMount{ID: 1, AccountID: 1}
	dir := &cloudDir{
		b: &backend{deps: Deps{}, mount: mount},
		item: domain.FileItem{
			ID:    "parent-id",
			Name:  "dir",
			IsDir: true,
		},
	}
	st := &stagingFile{
		b:           dir.b,
		name:        "movie.tmp",
		tempPath:    t.TempDir() + "/movie.tmp",
		uploads:     manager,
		taskID:      "task-1",
		parentID:    "parent-id",
		displayPath: "dir",
	}

	if errno := dir.renameStaging(context.Background(), st, dir, "movie.mp4"); errno != 0 {
		t.Fatalf("renameStaging 不应失败，errno=%v", errno)
	}
	if st.name != "movie.mp4" {
		t.Fatalf("节点名应更新为 movie.mp4，实际 %q", st.name)
	}
	if len(manager.renamed) != 1 || manager.renamed[0] != "task-1:movie.mp4" {
		t.Fatalf("应联动更新上传任务名，实际 %v", manager.renamed)
	}
}

func TestStagingUnlinkCancelsTask(t *testing.T) {
	manager := &recordingUploadManager{tempDir: t.TempDir()}
	mount := &domain.FuseMount{ID: 1, AccountID: 1}
	dir := &cloudDir{b: &backend{deps: Deps{}, mount: mount}}
	st := &stagingFile{
		b:        dir.b,
		name:     "movie.tmp",
		tempPath: t.TempDir() + "/movie.tmp",
		uploads:  manager,
		taskID:   "task-1",
	}

	if errno := dir.unlinkStaging(st); errno != 0 {
		t.Fatalf("unlinkStaging 不应失败，errno=%v", errno)
	}
	if len(manager.deleted) != 1 || manager.deleted[0] != "task-1" {
		t.Fatalf("应取消上传任务，实际 %v", manager.deleted)
	}
}

// staging 节点是否失效必须只看“上传任务还有没有希望”，不能看网盘列表：
// 列表存在最终一致性，把“只是还看不见”的节点当成失效会误删正在上传的文件。
func TestStagingStaleDetection(t *testing.T) {
	stranger := &recordingUploadManager{}
	cases := []struct {
		name  string
		node  *stagingFile
		stale bool
	}{
		{
			name:  "尚未入队且在保护期内",
			node:  &stagingFile{createdAt: time.Now()},
			stale: false,
		},
		{
			name:  "尚未入队且已超时",
			node:  &stagingFile{createdAt: time.Now().Add(-stagingOrphanTTL - time.Minute)},
			stale: true,
		},
		{
			name: "任务排队中",
			node: &stagingFile{createdAt: time.Now(), taskID: "task-1", uploads: &recordingUploadManager{
				tasks: map[string]*upload.Task{"task-1": {TaskID: "task-1", Status: upload.StatusPending}},
			}},
			stale: false,
		},
		{
			name: "任务上传中",
			node: &stagingFile{createdAt: time.Now(), taskID: "task-1", uploads: &recordingUploadManager{
				tasks: map[string]*upload.Task{"task-1": {TaskID: "task-1", Status: upload.StatusRunning}},
			}},
			stale: false,
		},
		{
			name: "任务已成功但列表还没追上",
			node: &stagingFile{createdAt: time.Now(), taskID: "task-1", uploads: &recordingUploadManager{
				tasks: map[string]*upload.Task{"task-1": {TaskID: "task-1", Status: upload.StatusSuccess}},
			}},
			stale: false,
		},
		{
			name: "任务失败",
			node: &stagingFile{createdAt: time.Now(), taskID: "task-1", uploads: &recordingUploadManager{
				tasks: map[string]*upload.Task{"task-1": {TaskID: "task-1", Status: upload.StatusFailed}},
			}},
			stale: true,
		},
		{
			name: "任务取消",
			node: &stagingFile{createdAt: time.Now(), taskID: "task-1", uploads: &recordingUploadManager{
				tasks: map[string]*upload.Task{"task-1": {TaskID: "task-1", Status: upload.StatusCanceled}},
			}},
			stale: true,
		},
		{
			name: "任务跳过",
			node: &stagingFile{createdAt: time.Now(), taskID: "task-1", uploads: &recordingUploadManager{
				tasks: map[string]*upload.Task{"task-1": {TaskID: "task-1", Status: upload.StatusSkipped}},
			}},
			stale: true,
		},
		{
			name:  "任务记录已消失但未超时",
			node:  &stagingFile{createdAt: time.Now(), taskID: "task-1", uploads: stranger},
			stale: false,
		},
		{
			name:  "任务记录已消失且已超时",
			node:  &stagingFile{createdAt: time.Now().Add(-stagingOrphanTTL - time.Minute), taskID: "task-1", uploads: stranger},
			stale: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.node.stale(context.Background()); got != c.stale {
				t.Fatalf("stale=%v，期望 %v", got, c.stale)
			}
		})
	}
}

func TestDropStagingNodeRecycles(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "stale-*")
	if err != nil {
		t.Fatalf("建临时文件失败: %v", err)
	}
	tempPath := tmp.Name()
	_ = tmp.Close()

	manager := &recordingUploadManager{tempDir: t.TempDir()}
	mount := &domain.FuseMount{ID: 1, AccountID: 1}
	dir := &cloudDir{b: &backend{deps: Deps{}, mount: mount}}
	st := &stagingFile{
		b:        dir.b,
		name:     "movie.mp4",
		tempPath: tempPath,
		uploads:  manager,
		taskID:   "task-1",
	}

	// 节点在本用例里没有真正挂到 dir 上，这里主要验证回收不会 panic 且清理彻底。
	dir.dropStagingNode(st)

	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("失效 staging 节点的临时文件应被删除，err=%v", err)
	}
	if len(manager.deleted) != 1 || manager.deleted[0] != "task-1" {
		t.Fatalf("应删除失效节点背后的上传任务，实际 %v", manager.deleted)
	}
	if ch := dir.GetChild("movie.mp4"); ch != nil {
		t.Fatalf("节点应从目录摘除")
	}
}

// 入队失败意味着临时文件永远不会变成云端文件：只打日志会把节点留在目录里，
// 之后同名 rename 报 EEXIST、copy 报 EROFS。
func TestReleaseRecyclesNodeWhenQueueFails(t *testing.T) {
	tmp, err := os.CreateTemp(t.TempDir(), "fail-*")
	if err != nil {
		t.Fatalf("建临时文件失败: %v", err)
	}
	tempPath := tmp.Name()

	manager := &recordingUploadManager{tempDir: t.TempDir(), createErr: errors.New("建任务失败")}
	mount := &domain.FuseMount{ID: 1, AccountID: 1}
	dir := &cloudDir{b: &backend{deps: Deps{}, mount: mount}}
	node := &stagingFile{
		b:         dir.b,
		name:      "movie.mp4",
		tempPath:  tempPath,
		parentDir: dir,
		createdAt: time.Now(),
	}
	handle := &stagingUploadHandle{
		node:      node,
		uploads:   manager,
		accountID: 1,
		parentID:  "parent-id",
		fileName:  "movie.mp4",
		tempPath:  tempPath,
		file:      tmp,
		flags:     syscall.O_WRONLY,
	}

	if errno := handle.Release(context.Background()); errno != 0 {
		t.Fatalf("Release 不应再把失败抛给内核，errno=%v", errno)
	}
	if len(manager.params) != 1 {
		t.Fatalf("应尝试建一次上传任务，实际 %d", len(manager.params))
	}
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("入队失败后临时文件应被删除，err=%v", err)
	}
}
