package crosstransfer

import (
	"context"
	"strings"
	"testing"

	"litepan/internal/domain"
)

// relDirOf 把扫描结果摊平成 文件名 → 目标侧相对目录，便于断言落点。
func relDirOf(t *testing.T, files []ScanFile) map[string]string {
	t.Helper()
	out := make(map[string]string, len(files))
	for _, f := range files {
		out[f.Name] = f.RelDir
	}
	return out
}

func testHash() map[domain.HashType]string {
	return map[domain.HashType]string{domain.HashMD5: "11111111111111111111111111111111"}
}

// 单选一个目录时，选区内容直接落进目标目录：目标下不得再重建一层同名目录。
func TestScanSingleRootLandsDirectlyInTarget(t *testing.T) {
	drv := newCleanupDriver()
	hash := testHash()
	drv.children["cloudadd"] = []domain.FileItem{
		{ID: "f1", Name: "a.mp4", Size: 1, Hash: hash},
		{ID: "f2", Name: "b.mp4", Size: 1, Hash: hash},
		{ID: "sub", Name: "合集", IsDir: true},
	}
	drv.children["sub"] = []domain.FileItem{
		{ID: "f3", Name: "c.mp4", Size: 1, Hash: hash},
	}
	service := newCleanupService(t, drv)

	result, err := service.ScanSources(context.Background(), 1, []ScanRoot{
		{ParentID: "cloudadd", DisplayPath: "/来自：云添加"},
	}, "md5")
	if err != nil {
		t.Fatalf("单目录扫描失败: %v", err)
	}

	want := map[string]string{"a.mp4": "", "b.mp4": "", "c.mp4": "合集"}
	got := relDirOf(t, result.Files)
	for name, dir := range want {
		if got[name] != dir {
			t.Fatalf("%s 的目标相对目录应为 %q，实际 %q（完整 %#v）", name, dir, got[name], got)
		}
	}

	// 树上同样不应出现选区目录本身：单选时 root 是透明的。
	for _, node := range result.Tree {
		if node.Type == "dir" && node.Name == "来自：云添加" {
			t.Fatalf("扫描树不应再包一层选区目录: %#v", result.Tree)
		}
	}

	relPaths := make(map[string]string, len(result.Files))
	for _, f := range result.Files {
		relPaths[f.Name] = f.RelPath
	}
	if relPaths["a.mp4"] != "a.mp4" || relPaths["c.mp4"] != "合集/c.mp4" {
		t.Fatalf("相对路径不应带选区目录前缀: %#v", relPaths)
	}
}

// 单目录秒传全链路：目标目录下不得创建任何新目录。
func TestExecuteSingleRootCreatesNoExtraDirectory(t *testing.T) {
	drv := newCleanupDriver()
	hash := testHash()
	drv.children["cloudadd"] = []domain.FileItem{
		{ID: "f1", Name: "a.mp4", Size: 1, Hash: hash},
		{ID: "f2", Name: "b.mp4", Size: 1, Hash: hash},
	}
	service := newCleanupService(t, drv)

	result, err := service.ScanSources(context.Background(), 1, []ScanRoot{
		{ParentID: "cloudadd", DisplayPath: "/来自：云添加"},
	}, "md5")
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	files := make([]TransferFile, 0, len(result.Files))
	for _, f := range result.Files {
		files = append(files, TransferFile{
			SourceFileID: f.SourceFileID,
			RelPath:      f.RelPath,
			RelDir:       f.RelDir,
			Name:         f.Name,
			Size:         f.Size,
			Hash:         f.Hash,
		})
	}

	if err := service.ExecuteStream(context.Background(), ExecuteInput{
		SourceAccountID:   1,
		TargetAccountID:   2,
		TargetParentID:    "target",
		TargetDisplayPath: "/jav",
		MethodID:          "md5",
		Files:             files,
		Conflict:          "skip",
	}, func(StreamEvent) error { return nil }); err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	for _, item := range drv.children["target"] {
		if item.IsDir {
			t.Fatalf("目标目录下不应创建子目录，实际创建了 %q", item.Name)
		}
	}
}

// 多选目录时保留各自目录名：与扫描树上显示的目录节点一致，也避免同名文件互相覆盖。
func TestScanMultiRootKeepsDirectoryNames(t *testing.T) {
	drv := newCleanupDriver()
	hash := testHash()
	drv.children["movies"] = []domain.FileItem{
		{ID: "movie-file", Name: "movie.mkv", Size: 1, Hash: hash},
	}
	drv.children["music"] = []domain.FileItem{
		{ID: "music-file", Name: "song.flac", Size: 1, Hash: hash},
	}
	service := newCleanupService(t, drv)

	result, err := service.ScanSources(context.Background(), 1, []ScanRoot{
		{ParentID: "movies", DisplayPath: "/媒体/电影"},
		{ParentID: "music", DisplayPath: "/媒体/音乐"},
	}, "md5")
	if err != nil {
		t.Fatalf("多目录扫描失败: %v", err)
	}

	want := map[string]string{"movie.mkv": "电影", "song.flac": "音乐"}
	got := relDirOf(t, result.Files)
	for name, dir := range want {
		if got[name] != dir {
			t.Fatalf("%s 的目标相对目录应为 %q，实际 %q（完整 %#v）", name, dir, got[name], got)
		}
	}
}

// 普传链路必须与秒传一致：单选目录时同样不重建同名目录。
func TestPlainSingleRootLandsDirectlyInTarget(t *testing.T) {
	drv := newCleanupDriver()
	drv.children["cloudadd"] = []domain.FileItem{
		{ID: "f1", Name: "a.mp4", Size: 1},
		{ID: "sub", Name: "合集", IsDir: true},
	}
	drv.children["sub"] = []domain.FileItem{{ID: "f2", Name: "c.mp4", Size: 1}}
	service := newCleanupService(t, drv)

	scan, err := service.enumeratePlainSourcesProgress(context.Background(), 1, []ScanRoot{
		{ParentID: "cloudadd", DisplayPath: "/来自：云添加"},
	}, nil)
	if err != nil {
		t.Fatalf("普传枚举失败: %v", err)
	}

	want := map[string]string{"a.mp4": "", "c.mp4": "合集"}
	for _, f := range scan.files {
		if f.relDir != want[f.name] {
			t.Fatalf("%s 的目标相对目录应为 %q，实际 %q", f.name, want[f.name], f.relDir)
		}
	}
}

// 报错信息仍要带完整展示路径，剥掉的是目标落点，不是诊断信息。
func TestScanSingleRootErrorKeepsDisplayPath(t *testing.T) {
	drv := newCleanupDriver()
	drv.children["cloudadd"] = []domain.FileItem{{ID: "broken", Name: "损坏目录", IsDir: true}}
	drv.listErrors["broken"] = domain.Errorf(domain.CodeInternal, "上游列表失败")
	service := newCleanupService(t, drv)

	_, err := service.ScanSources(context.Background(), 1, []ScanRoot{
		{ParentID: "cloudadd", DisplayPath: "/来自：云添加"},
	}, "md5")
	if err == nil {
		t.Fatal("应返回目录读取错误")
	}
	if got := err.Error(); !strings.Contains(got, "/来自：云添加/损坏目录") {
		t.Fatalf("错误信息应保留完整展示路径，实际 %q", got)
	}
}
