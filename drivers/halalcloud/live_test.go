//go:build live

package halalcloud

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"litepan/internal/driver"
)

// 真实环境联调：需要 2dland 的 Client ID / Client Secret，用环境变量传，不写死也不入日志。
//
//	HALAL_CLIENT_ID=xxx HALAL_CLIENT_SECRET=yyy \
//	  go test -tags live ./drivers/halalcloud/ -run TestLiveSmoke -v -timeout 10m
//
// 步骤：建客户端 → 拉用户信息(Ping) → 列根目录 → 建测试目录 → 上传 1KB
//      → 回读确认 → 取直链 → 回收站删除 → 清理测试目录。
// 任何一步失败都会打印该步的原始错误，方便定位是协议问题还是权限问题。
func TestLiveSmoke(t *testing.T) {
	clientID := os.Getenv("HALAL_CLIENT_ID")
	clientSecret := os.Getenv("HALAL_CLIENT_SECRET")
	if clientID == "" || clientSecret == "" {
		t.Skip("未设置 HALAL_CLIENT_ID / HALAL_CLIENT_SECRET，跳过真实环境联调")
	}
	root := os.Getenv("HALAL_TEST_ROOT")
	if root == "" {
		root = "/"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	d := &Driver{}
	d.add = Addition{ClientID: clientID, ClientSecret: clientSecret, RootPath: root, Timeout: 60}
	if err := d.Init(ctx); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	defer d.Drop(context.Background())
	t.Logf("Init 成功，host=%s root=%s", normalizeHost(d.add.Host), d.rootPath())

	if err := d.Ping(ctx); err != nil {
		t.Fatalf("Ping（拉用户信息）失败，Client ID/Secret 可能不对: %v", err)
	}
	t.Log("Ping 成功（Client ID/Secret 有效）")

	items, err := d.ListFiles(ctx, "/")
	if err != nil {
		t.Fatalf("ListFiles 根目录失败: %v", err)
	}
	t.Logf("根目录有 %d 项", len(items))
	for i, it := range items {
		if i >= 5 {
			break
		}
		t.Logf("  - %s dir=%v size=%d id=%q", it.Name, it.IsDir, it.Size, it.ID)
	}
	if len(items) == 0 {
		t.Skip("根目录为空，后续用例需要至少一个可写目录")
	}

	// 找一个目录做测试父目录
	parent := ""
	for _, it := range items {
		if it.IsDir {
			parent = it.ID
			break
		}
	}
	if parent == "" {
		t.Skip("根目录下没有子目录，跳过写入用例")
	}
	stamp := time.Now().Format("150405")
	dirName := "_litepan_probe_" + stamp
	t.Logf("测试父目录: %s", parent)

	created, err := d.CreateFolder(ctx, parent, dirName)
	if err != nil {
		t.Fatalf("CreateFolder 失败（dir 标记可能不被服务端接受）: %v", err)
	}
	t.Logf("CreateFolder 成功: id=%q name=%q", created.ID, created.Name)
	cleanupDir := created.ID
	defer func() {
		if cleanupDir == "" {
			return
		}
		if err := d.DeleteFiles(context.Background(), []string{cleanupDir}); err != nil {
			t.Logf("清理测试目录失败（请手动删除 %s）: %v", cleanupDir, err)
			return
		}
		t.Logf("已清理测试目录 %s（进回收站）", cleanupDir)
	}()

	// 上传 1KB
	tmp := filepath.Join(t.TempDir(), "probe.txt")
	payload := make([]byte, 1024)
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		t.Fatalf("写本地临时文件失败: %v", err)
	}
	res, err := d.UploadLocalFile(ctx, localUpload(tmp, "probe.txt", created.ID))
	if err != nil {
		t.Fatalf("UploadLocalFile 失败: %v", err)
	}
	t.Logf("上传成功: id=%q size=%d msg=%q", res.FileID, res.Size, res.Message)

	// 回读确认
	list, err := d.ListFiles(ctx, created.ID)
	if err != nil {
		t.Fatalf("回读测试目录失败: %v", err)
	}
	found := false
	for _, it := range list {
		if it.Name == "probe.txt" {
			found = true
			t.Logf("回读命中: size=%d（期望 1024）", it.Size)
			if it.Size != 1024 {
				t.Errorf("文件大小不符: got %d want 1024", it.Size)
			}
		}
	}
	if !found {
		t.Fatalf("上传后列表里没有 probe.txt，实际: %v", list)
	}

	// 直链
	info, err := d.ResolveDownload(ctx, downloadReq(res.FileID))
	if err != nil {
		t.Errorf("ResolveDownload 失败: %v", err)
	} else {
		host := info.URL
		if len(host) > 60 {
			host = host[:60] + "..."
		}
		t.Logf("直链获取成功: %s（过期 %v）", host, info.Expiration)
	}

	// 秒传验证：同样内容再传一次，服务端已有相同 CID 应当零传输
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		t.Fatalf("重写本地文件失败: %v", err)
	}
	start := time.Now()
	if _, err := d.UploadLocalFile(ctx, localUpload(tmp, "probe-second.txt", created.ID)); err != nil {
		t.Errorf("第二次上传失败: %v", err)
	} else {
		t.Logf("第二次上传（同内容）耗时 %v —— 若明显低于首次即为秒传生效", time.Since(start).Round(time.Millisecond))
	}
	fmt.Println("SMOKE OK")
}

func localUpload(localPath, name, parentID string) driver.LocalUploadRequest {
	return driver.LocalUploadRequest{LocalPath: localPath, FileName: name, ParentID: parentID, ConflictPolicy: "overwrite"}
}

func downloadReq(fileID string) driver.DownloadRequest {
	return driver.DownloadRequest{FileID: fileID}
}
