package webdav

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"litepan/internal/domain"
	"litepan/internal/driver"
	"litepan/internal/driver/uploadutil"
	"litepan/internal/httpx"
)

func (d *Driver) UploadLocalFile(ctx context.Context, req driver.LocalUploadRequest) (*driver.LocalUploadResult, error) {
	targetName := strings.TrimSpace(req.FileName)
	if targetName == "" {
		return nil, domain.Errorf(domain.CodeValidation, "上传文件名不能为空")
	}
	if err := uploadutil.ValidateFileName(targetName); err != nil {
		return nil, err
	}
	localFile, err := uploadutil.StatLocalFile(req.LocalPath)
	if err != nil {
		return nil, err
	}
	localPath := localFile.Path
	fileSize := localFile.Size

	parentID := d.normalizePath(req.ParentID)
	policy := uploadutil.NormalizeConflictPolicy(req.ConflictPolicy)
	resolvedName, skipped, err := d.prepareUploadTarget(ctx, parentID, targetName, policy)
	if err != nil {
		return nil, err
	}
	if skipped {
		return &driver.LocalUploadResult{
			ParentID: parentID,
			FileName: targetName,
			Size:     fileSize,
			Message:  fmt.Sprintf("文件 '%s' 已存在，已跳过", targetName),
			Skipped:  true,
		}, nil
	}
	targetName = resolvedName
	targetPath := d.childPath(parentID, targetName)

	// 其余冲突策略已由 prepareUploadTarget 解析出唯一文件名；overwrite 直接覆盖。
	src, err := os.Open(localPath)
	if err != nil {
		return nil, domain.Wrap(domain.CodeDriverError, err)
	}
	defer src.Close()

	reader := &uploadutil.ReadProgress{
		R:     src,
		Total: fileSize,
		OnProgress: func(uploaded int64) {
			uploadutil.NotifyProgress(req.OnProgress, uploaded, fileSize, "正在上传到 WebDAV")
		},
	}
	if err := d.putStream(ctx, targetPath, reader, fileSize); err != nil {
		return nil, err
	}
	uploadutil.NotifyProgress(req.OnProgress, fileSize, fileSize, "上传成功")
	return &driver.LocalUploadResult{
		FileID:   targetPath,
		ParentID: parentID,
		FileName: targetName,
		Size:     fileSize,
		Message:  fmt.Sprintf("文件 '%s' 上传成功", targetName),
	}, nil
}

func (d *Driver) prepareUploadTarget(ctx context.Context, parentID, name, policy string) (string, bool, error) {
	if policy == "overwrite" {
		return name, false, nil
	}
	c, err := d.ensureClient()
	if err != nil {
		return "", false, err
	}
	_ = ctx
	infos, err := c.ReadDir(d.normalizePath(parentID))
	if err != nil {
		return "", false, mapError(err)
	}
	existing := make(map[string]struct{}, len(infos))
	hasSame := false
	for _, fi := range infos {
		existing[fi.Name()] = struct{}{}
		if fi.Name() == name {
			if fi.IsDir() {
				return "", false, domain.Errorf(domain.CodeValidation, "目标目录已存在同名文件夹 '%s'", name)
			}
			hasSame = true
		}
	}
	if !hasSame {
		return name, false, nil
	}
	switch policy {
	case "skip":
		return name, true, nil
	case "fail":
		return "", false, domain.Errorf(domain.CodeValidation, "目标目录已存在同名文件 '%s'", name)
	case "keep_both", "keep_both_new", "rename":
		return uploadutil.KeepBothName(name, existing), false, nil
	default:
		return name, false, nil
	}
}

// putStream 发一条带 Content-Length 的 PUT 上传。
//
// 刻意不走 gowebdav 的 WriteStream：
//  1. 它对非 io.Seeker 的流会把整个文件先读进内存（1MB 起步、按需扩容，
//     通过 FUSE 写 5GB 就是 5GB 内存），大文件直接 OOM；
//  2. 失败时只返回状态码，服务端的响应正文被丢掉，现场排查只能看到
//     "WriteStream /path: 409" 这种毫无信息量的报错。
//
// 这里直接复用驱动已有的上传专用 HTTP 客户端（流式、可关 HTTP/2），
// 并把非 2xx 的响应正文截断后带进错误里。
func (d *Driver) putStream(ctx context.Context, targetPath string, body io.Reader, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, d.resourceURL(targetPath), body)
	if err != nil {
		return domain.Wrap(domain.CodeDriverError, err)
	}
	if size >= 0 {
		req.ContentLength = size
	}
	req.Header.Set("User-Agent", httpx.DefaultUserAgent)
	req.Header.Set("Content-Type", "application/octet-stream")
	if strings.TrimSpace(d.add.Username) != "" || strings.TrimSpace(d.add.Password) != "" {
		req.SetBasicAuth(d.add.Username, d.add.Password)
	}
	client := d.uploadHTTP
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return domain.Wrap(domain.CodeDriverError, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	detail := ""
	if raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 512)); readErr == nil {
		detail = strings.TrimSpace(string(raw))
	}
	if detail == "" {
		return domain.Errorf(domain.CodeDriverError, "PUT %s 失败: %s", targetPath, resp.Status)
	}
	return domain.Errorf(domain.CodeDriverError, "PUT %s 失败: %s: %s", targetPath, resp.Status, detail)
}
