package file

import (
	"context"
	"strings"
	"testing"

	"litepan/internal/core/driverexec"
	"litepan/internal/domain"
	"litepan/internal/driver"
)

// memFSDriver 是内存网盘，IDPath 语义（fileID 就是路径），用来复现真实驱动的行为：
// 移动之后旧路径立即失效，再拿旧 fileID 去改名必然找不到源对象。
type memFSDriver struct {
	dirs       map[string][]domain.FileItem
	moves      int
	renames    []string
	renameNoop bool  // 模拟「改名接口返回成功，但云端其实没改」
	renameErr  error // 模拟改名接口直接报错
}

func newMemFSDriver() *memFSDriver {
	return &memFSDriver{dirs: map[string][]domain.FileItem{
		"/src": {{ID: "/src/a.mp4", Name: "a.mp4", IDKind: domain.IDPath}},
		"/dst": {},
	}}
}

func (d *memFSDriver) Config() driver.Config      { return driver.Config{Name: "memfs"} }
func (d *memFSDriver) GetAddition() any           { return struct{}{} }
func (d *memFSDriver) Init(context.Context) error { return nil }
func (d *memFSDriver) Drop(context.Context) error { return nil }
func (d *memFSDriver) Ping(context.Context) error { return nil }

func (d *memFSDriver) ListFiles(_ context.Context, parentID string) ([]domain.FileItem, error) {
	return append([]domain.FileItem(nil), d.dirs[parentID]...), nil
}

func (d *memFSDriver) MoveFiles(_ context.Context, fileIDs []string, targetParentID, sourceParentID string) error {
	d.moves++
	for _, id := range fileIDs {
		idx := indexOf(d.dirs[sourceParentID], id)
		if idx < 0 {
			return domain.Errorf(domain.CodeNotFound, "move: %s 不在 %s", id, sourceParentID)
		}
		item := d.dirs[sourceParentID][idx]
		d.dirs[sourceParentID] = append(d.dirs[sourceParentID][:idx], d.dirs[sourceParentID][idx+1:]...)
		item.ID = childID(targetParentID, item.Name)
		d.dirs[targetParentID] = append(d.dirs[targetParentID], item)
	}
	return nil
}

func (d *memFSDriver) RenameFile(_ context.Context, fileID, newName string) error {
	d.renames = append(d.renames, fileID+" -> "+newName)
	if d.renameErr != nil {
		return d.renameErr
	}
	parent := parentIDOf(fileID)
	idx := indexOf(d.dirs[parent], fileID)
	if idx < 0 {
		// 真实驱动（OpenList / WebDAV）就是这个反馈：源路径已不存在。
		return domain.Errorf(domain.CodeNotFound, "rename: 源对象不存在 %s", fileID)
	}
	if d.renameNoop {
		return nil
	}
	d.dirs[parent][idx].Name = newName
	d.dirs[parent][idx].ID = childID(parent, newName)
	return nil
}

// singleShotDriver 在原驱动之上补上原生单步能力，用于验证优先路径。
type singleShotDriver struct{ *memFSDriver }

func (d *singleShotDriver) MoveAndRename(_ context.Context, fileID, targetParentID, newName string) error {
	src := parentIDOf(fileID)
	idx := indexOf(d.dirs[src], fileID)
	if idx < 0 {
		return domain.Errorf(domain.CodeNotFound, "move+rename: 源对象不存在 %s", fileID)
	}
	item := d.dirs[src][idx]
	d.dirs[src] = append(d.dirs[src][:idx], d.dirs[src][idx+1:]...)
	item.Name = newName
	item.ID = childID(targetParentID, newName)
	d.dirs[targetParentID] = append(d.dirs[targetParentID], item)
	return nil
}

func childID(parent, name string) string {
	return strings.TrimSuffix(parent, "/") + "/" + name
}

func parentIDOf(fileID string) string {
	idx := strings.LastIndex(fileID, "/")
	if idx <= 0 {
		return "/"
	}
	return fileID[:idx]
}

func indexOf(items []domain.FileItem, id string) int {
	for i := range items {
		if items[i].ID == id {
			return i
		}
	}
	return -1
}

func newMemService(drv driver.Driver) *Service {
	return NewService(driverexec.New(uploadRootProvider{drv: drv}, nil), nil, nil, nil, nil, nil)
}

// 关键回归：改名必须用「移动之后」的新 fileID。
// 修复前用的是移动前的 /src/a.mp4，改名必然失败（或被吞掉），
// 结果就是「文件移到目标目录了、名字没改、调用方却收到成功」。
func TestMoveAndRenameUsesPostMoveID(t *testing.T) {
	drv := newMemFSDriver()
	svc := newMemService(drv)

	newID, err := svc.MoveAndRename(context.Background(), 1, "/src/a.mp4", "a.mp4", "/src", "/dst", "b.mp4")
	if err != nil {
		t.Fatalf("MoveAndRename() error = %v", err)
	}
	if newID != "/dst/b.mp4" {
		t.Fatalf("MoveAndRename() newID = %q, want /dst/b.mp4", newID)
	}
	if len(drv.renames) != 1 || drv.renames[0] != "/dst/a.mp4 -> b.mp4" {
		t.Fatalf("改名应当使用移动后的新 ID，实际调用 = %#v", drv.renames)
	}
	if len(drv.dirs["/src"]) != 0 {
		t.Fatalf("源目录应当已清空，实际 %#v", drv.dirs["/src"])
	}
	if len(drv.dirs["/dst"]) != 1 || drv.dirs["/dst"][0].Name != "b.mp4" || drv.dirs["/dst"][0].ID != "/dst/b.mp4" {
		t.Fatalf("目标目录状态不对：%#v", drv.dirs["/dst"])
	}
}

// 驱动声称改名成功但云端没动：必须靠最终校验发现，并且不能返回成功。
func TestMoveAndRenameRejectsSilentRenameFailure(t *testing.T) {
	drv := newMemFSDriver()
	drv.renameNoop = true
	svc := newMemService(drv)

	newID, err := svc.MoveAndRename(context.Background(), 1, "/src/a.mp4", "a.mp4", "/src", "/dst", "b.mp4")
	if err == nil {
		t.Fatalf("改名未生效时必须报错，实际返回成功 newID=%q", newID)
	}
	if newID != "" {
		t.Fatalf("失败时不应返回 fileID，实际 %q", newID)
	}
	ae, ok := domain.AsAppError(err)
	if !ok || ae.Code != domain.CodeDriverError {
		t.Fatalf("期望 DRIVER_ERROR，实际 %v", err)
	}
	// 口口声声改成功但云端没动：回滚要能靠「旧名字」把文件移回源目录，不能停在中间态。
	if len(drv.dirs["/dst"]) != 0 {
		t.Fatalf("回滚后目标目录应为空，实际 %#v", drv.dirs["/dst"])
	}
	if len(drv.dirs["/src"]) != 1 || drv.dirs["/src"][0].Name != "a.mp4" {
		t.Fatalf("回滚后源目录应恢复原状态，实际 %#v", drv.dirs["/src"])
	}
}

// 改名报错要走回滚：文件必须回到源目录，不能停在「已移动、未改名」的中间态。
func TestMoveAndRenameRollsBackWhenRenameFails(t *testing.T) {
	drv := newMemFSDriver()
	drv.renameErr = domain.Errorf(domain.CodeDriverError, "改名被拒")
	svc := newMemService(drv)

	if _, err := svc.MoveAndRename(context.Background(), 1, "/src/a.mp4", "a.mp4", "/src", "/dst", "b.mp4"); err == nil {
		t.Fatal("改名失败却返回成功")
	}
	if len(drv.dirs["/dst"]) != 0 {
		t.Fatalf("回滚后目标目录应为空，实际 %#v", drv.dirs["/dst"])
	}
	if len(drv.dirs["/src"]) != 1 || drv.dirs["/src"][0].Name != "a.mp4" {
		t.Fatalf("回滚后源目录应恢复原名，实际 %#v", drv.dirs["/src"])
	}
}

// 驱动原生支持单步移动+改名时应优先走单步，不产生「移动 → 改名」两次调用与中间态。
func TestMoveAndRenamePrefersSingleShotCapability(t *testing.T) {
	base := newMemFSDriver()
	svc := newMemService(&singleShotDriver{memFSDriver: base})

	newID, err := svc.MoveAndRename(context.Background(), 1, "/src/a.mp4", "a.mp4", "/src", "/dst", "b.mp4")
	if err != nil {
		t.Fatalf("MoveAndRename() error = %v", err)
	}
	if newID != "/dst/b.mp4" {
		t.Fatalf("MoveAndRename() newID = %q", newID)
	}
	if base.moves != 0 || len(base.renames) != 0 {
		t.Fatalf("不应回退到两步实现，moves=%d renames=%#v", base.moves, base.renames)
	}
	if len(base.dirs["/dst"]) != 1 || base.dirs["/dst"][0].Name != "b.mp4" {
		t.Fatalf("目标目录状态不对：%#v", base.dirs["/dst"])
	}
}

// 同目录只改名：不应触发移动。
func TestMoveAndRenameSameParentOnlyRenames(t *testing.T) {
	drv := newMemFSDriver()
	svc := newMemService(drv)

	newID, err := svc.MoveAndRename(context.Background(), 1, "/src/a.mp4", "a.mp4", "/src", "/src", "c.mp4")
	if err != nil {
		t.Fatalf("MoveAndRename() error = %v", err)
	}
	if drv.moves != 0 {
		t.Fatalf("同目录改名不应调用 MoveFiles，实际 %d 次", drv.moves)
	}
	if newID != "/src/c.mp4" {
		t.Fatalf("newID = %q, want /src/c.mp4", newID)
	}
}
