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

func relPathOf(t *testing.T, files []ScanFile) map[string]string {
	t.Helper()
	out := make(map[string]string, len(files))
	for _, f := range files {
		out[f.Name] = f.RelPath
	}
	return out
}

func testHash() map[domain.HashType]string {
	return map[domain.HashType]string{domain.HashMD5: "11111111111111111111111111111111"}
}

// 选什么就传什么：选中的目录名必须进目标侧路径，其内子目录结构同样保留。
func TestScanSingleRootKeepsSelectedDirectory(t *testing.T) {
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

	wantDir := map[string]string{
		"a.mp4": "来自：云添加",
		"b.mp4": "来自：云添加",
		"c.mp4": "来自：云添加/合集",
	}
	gotDir := relDirOf(t, result.Files)
	for name, dir := range wantDir {
		if gotDir[name] != dir {
			t.Fatalf("%s 的目标相对目录应为 %q，实际 %q", name, dir, gotDir[name])
		}
	}

	wantPath := map[string]string{
		"a.mp4": "来自：云添加/a.mp4",
		"c.mp4": "来自：云添加/合集/c.mp4",
	}
	gotPath := relPathOf(t, result.Files)
	for name, path := range wantPath {
		if gotPath[name] != path {
			t.Fatalf("%s 的相对路径应为 %q，实际 %q", name, path, gotPath[name])
		}
	}
}

// 秒传全链路：目标下重建选中的目录，文件落在该目录（子目录同样重建）里。
func TestExecuteSingleRootRecreatesSelectedDirectory(t *testing.T) {
	drv := &rapidHitDriver{cleanupDriver: newCleanupDriver()}
	drv.children["cloudadd"] = []domain.FileItem{
		{ID: "f1", Name: "a.mp4", Size: 1, Hash: testHash()},
		{ID: "f2", Name: "b.mp4", Size: 1, Hash: testHash()},
		{ID: "sub", Name: "合集", IsDir: true},
	}
	drv.children["sub"] = []domain.FileItem{
		{ID: "f3", Name: "c.mp4", Size: 1, Hash: testHash()},
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

	folderID := ""
	for _, item := range drv.children["target"] {
		if item.IsDir && item.Name == "来自：云添加" {
			folderID = item.ID
		}
	}
	if folderID == "" {
		t.Fatalf("目标下应重建选中的目录，actual=%#v", drv.children["target"])
	}
	names := map[string]bool{}
	for _, item := range drv.children[folderID] {
		names[item.Name] = item.IsDir
	}
	for _, want := range []string{"a.mp4", "b.mp4"} {
		if isDir, ok := names[want]; !ok || isDir {
			t.Fatalf("选中的目录下应有文件 %q，actual=%#v", want, names)
		}
	}

	// 选区内的子目录也要在目标侧重建，且文件落在子目录里（不能拍平）。
	subID := ""
	for _, item := range drv.children[folderID] {
		if item.IsDir && item.Name == "合集" {
			subID = item.ID
		}
	}
	if subID == "" {
		t.Fatalf("选中的目录下应重建子目录 合集，actual=%#v", drv.children[folderID])
	}
	if len(drv.children[subID]) != 1 || drv.children[subID][0].Name != "c.mp4" {
		t.Fatalf("子目录 合集 下应有且仅有 c.mp4，actual=%#v", drv.children[subID])
	}
}

// 多选目录时同样保留各自目录名，与扫描树上显示的目录节点一致。
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

// 普传链路必须与秒传一致：单选目录时同样重建同名目录。
func TestPlainSingleRootKeepsSelectedDirectory(t *testing.T) {
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

	want := map[string]string{"a.mp4": "来自：云添加", "c.mp4": "来自：云添加/合集"}
	for _, f := range scan.files {
		if f.relDir != want[f.name] {
			t.Fatalf("%s 的目标相对目录应为 %q，实际 %q", f.name, want[f.name], f.relDir)
		}
	}
}

// 报错信息要带完整展示路径（含选区目录名）。
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

// infoDriver 给 cleanupDriver 补上 InfoGetter：选中单个文件作为源时，
// 扫描需要按 ID 取文件元数据（目录扫描只用 ListFiles）。
type infoDriver struct {
	*cleanupDriver
}

func (d *infoDriver) GetFileInfo(_ context.Context, fileID string) (*domain.FileItem, error) {
	for _, items := range d.children {
		for i := range items {
			if items[i].ID == fileID {
				return &items[i], nil
			}
		}
	}
	return nil, domain.Errorf(domain.CodeNotFound, "文件不存在: %s", fileID)
}

// fileRapidDriver：既会命中秒传，也能按 ID 返回文件信息（文件根端到端用例用）。
type fileRapidDriver struct {
	*rapidHitDriver
}

func (d *fileRapidDriver) GetFileInfo(_ context.Context, fileID string) (*domain.FileItem, error) {
	for _, items := range d.children {
		for i := range items {
			if items[i].ID == fileID {
				return &items[i], nil
			}
		}
	}
	return nil, domain.Errorf(domain.CodeNotFound, "文件不存在: %s", fileID)
}

// 选中的是文件：直接落目标目录，relDir 为空，树上只有一条文件记录。
func TestScanFileRootLandsFlatInTarget(t *testing.T) {
	drv := newCleanupDriver()
	drv.children["cloudadd"] = []domain.FileItem{
		{ID: "f1", Name: "Skins.S02E01.mkv", Size: 7, Hash: testHash()},
	}
	service := newCleanupService(t, &infoDriver{cleanupDriver: drv})

	result, err := service.ScanSources(context.Background(), 1, []ScanRoot{
		{ParentID: "f1", DisplayPath: "/来自：云添加/皮囊 (2007)/Skins.S02E01.mkv", IsFile: true},
	}, "md5")
	if err != nil {
		t.Fatalf("文件根扫描失败: %v", err)
	}
	if result.Total != 1 || len(result.Files) != 1 {
		t.Fatalf("应只有 1 个文件，actual=%#v", result.Files)
	}
	got := result.Files[0]
	if got.Name != "Skins.S02E01.mkv" || got.RelDir != "" || got.RelPath != "Skins.S02E01.mkv" {
		t.Fatalf("文件应平铺落目标目录，actual name=%q relDir=%q relPath=%q", got.Name, got.RelDir, got.RelPath)
	}
	if !got.Eligible {
		t.Fatalf("带 MD5 的文件应可秒传: %#v", got)
	}
	if len(result.Tree) != 1 || result.Tree[0].Type != "file" {
		t.Fatalf("树的顶层应只有一个文件节点，actual=%#v", result.Tree)
	}
	if result.Tree[0].RelPath != "Skins.S02E01.mkv" {
		t.Fatalf("树节点 rel_path 应平铺，actual=%q", result.Tree[0].RelPath)
	}
}

// 目录 + 文件混选：目录保留目录名，文件平铺。
func TestScanMixedRootsFlattenFilesKeepDirs(t *testing.T) {
	drv := newCleanupDriver()
	drv.children["season"] = []domain.FileItem{
		{ID: "c", Name: "c.mp4", Size: 1, Hash: testHash()},
	}
	drv.children["cloudadd"] = []domain.FileItem{
		{ID: "a", Name: "a.mp4", Size: 1, Hash: testHash()},
	}
	service := newCleanupService(t, &infoDriver{cleanupDriver: drv})

	result, err := service.ScanSources(context.Background(), 1, []ScanRoot{
		{ParentID: "season", DisplayPath: "/来自：云添加/合集"},
		{ParentID: "a", DisplayPath: "/来自：云添加/a.mp4", IsFile: true},
	}, "md5")
	if err != nil {
		t.Fatalf("混选扫描失败: %v", err)
	}
	want := map[string]string{"c.mp4": "合集", "a.mp4": ""}
	got := relDirOf(t, result.Files)
	for name, dir := range want {
		if got[name] != dir {
			t.Fatalf("%s 的落点应为 %q，实际 %q", name, dir, got[name])
		}
	}
	if len(result.Tree) != 2 {
		t.Fatalf("顶层应有目录节点 + 文件节点，actual=%#v", result.Tree)
	}
}

// 目录里已包含的文件又被单独选中：去重后只按目录扫，不重复计入。
func TestScanFileRootCoveredBySelectedDir(t *testing.T) {
	drv := newCleanupDriver()
	drv.children["cloudadd"] = []domain.FileItem{
		{ID: "a", Name: "a.mp4", Size: 1, Hash: testHash()},
	}
	service := newCleanupService(t, &infoDriver{cleanupDriver: drv})

	result, err := service.ScanSources(context.Background(), 1, []ScanRoot{
		{ParentID: "cloudadd", DisplayPath: "/来自：云添加"},
		{ParentID: "a", DisplayPath: "/来自：云添加/a.mp4", IsFile: true},
	}, "md5")
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("同一文件不应重复计入，actual=%d %#v", result.Total, result.Files)
	}
	if result.Files[0].RelDir != "来自：云添加" {
		t.Fatalf("应按目录根的落点处理，actual relDir=%q", result.Files[0].RelDir)
	}
}

// 平铺落点下同名文件会互相覆盖，选择阶段就拒绝。
func TestScanRejectsDuplicateFileNames(t *testing.T) {
	service := newCleanupService(t, newCleanupDriver())
	_, err := service.ScanSources(context.Background(), 1, []ScanRoot{
		{ParentID: "f1", DisplayPath: "/A/dup.mp4", IsFile: true},
		{ParentID: "f2", DisplayPath: "/B/dup.mp4", IsFile: true},
	}, "md5")
	if err == nil || !strings.Contains(err.Error(), "同名项") {
		t.Fatalf("同名文件应被拒绝，得到 %v", err)
	}
}

// 文件根走完秒传全链路：目标目录下只有文件，不建任何目录。
func TestExecuteFileRootCreatesNoDirectory(t *testing.T) {
	drv := &fileRapidDriver{rapidHitDriver: &rapidHitDriver{cleanupDriver: newCleanupDriver()}}
	drv.children["cloudadd"] = []domain.FileItem{
		{ID: "f1", Name: "a.mp4", Size: 4, Hash: testHash()},
	}
	service := newCleanupService(t, drv)

	result, err := service.ScanSources(context.Background(), 1, []ScanRoot{
		{ParentID: "f1", DisplayPath: "/来自：云添加/a.mp4", IsFile: true},
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
	children := drv.children["target"]
	if len(children) != 1 || children[0].IsDir || children[0].Name != "a.mp4" {
		t.Fatalf("目标目录下应只有 a.mp4，actual=%#v", children)
	}
}

// 普传链路：文件根同样平铺入队。
func TestPlainFileRootEnqueuesFlat(t *testing.T) {
	drv := newCleanupDriver()
	drv.children["cloudadd"] = []domain.FileItem{{ID: "f1", Name: "a.mp4", Size: 9}}
	service := newCleanupService(t, &infoDriver{cleanupDriver: drv})

	scan, err := service.enumeratePlainSourcesProgress(context.Background(), 1, []ScanRoot{
		{ParentID: "f1", DisplayPath: "/来自：云添加/a.mp4", IsFile: true},
	}, nil)
	if err != nil {
		t.Fatalf("普传枚举失败: %v", err)
	}
	if len(scan.files) != 1 {
		t.Fatalf("应有 1 个文件，actual=%#v", scan.files)
	}
	if f := scan.files[0]; f.relDir != "" || f.relPath != "a.mp4" || f.size != 9 {
		t.Fatalf("普传应平铺落点，actual relDir=%q relPath=%q size=%d", f.relDir, f.relPath, f.size)
	}
}
