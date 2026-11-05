package halalcloud

import (
	"context"
	"strings"
	"time"

	sdkModel "github.com/halalcloud/golang-sdk-lite/halalcloud/model"
	sdkUserFile "github.com/halalcloud/golang-sdk-lite/halalcloud/services/userfile"

	"litepan/internal/domain"
	"litepan/internal/driver"
)

// listPageLimit 单页条数；2dland 用 token 翻页，必须循环拉完，
// 否则大目录只返回首页，FUSE 侧会出现"云端有这个目录但列表里查不到"的错乱。
const listPageLimit = 100

func fileToItem(f *sdkUserFile.File) domain.FileItem {
	mod := time.UnixMilli(f.UpdateTs)
	if f.UpdateTs <= 0 {
		mod = time.UnixMilli(f.CreateTs)
	}
	return domain.FileItem{
		ID:      f.Path,
		Name:    f.Name,
		Size:    f.Size,
		IsDir:   f.Dir,
		ModTime: mod,
		IDKind:  domain.IDPath,
	}
}

func errNotInit() error {
	return domain.Errorf(domain.CodeInternal, "2dland 客户端未初始化")
}

// mapError 把 SDK 错误映射成 LitePan 的结构化错误码（HTTP 中间件据此给状态码）。
func mapError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "401"), strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "invalid token"), strings.Contains(lower, "token expired"):
		return domain.Errorf(domain.CodeAuthExpired, "2dland 认证失效：%s", msg)
	case strings.Contains(lower, "403"), strings.Contains(lower, "forbidden"):
		return domain.Errorf(domain.CodePermissionDenied, "2dland 拒绝访问：%s", msg)
	case strings.Contains(lower, "404"), strings.Contains(lower, "not found"), strings.Contains(lower, "not exist"):
		return domain.Errorf(domain.CodeNotFound, "2dland 路径不存在：%s", msg)
	case strings.Contains(lower, "429"), strings.Contains(lower, "too many"):
		return domain.Errorf(domain.CodeRateLimited, "2dland 限流：%s", msg)
	default:
		return domain.Wrap(domain.CodeDriverError, err)
	}
}

func (d *Driver) ListFiles(ctx context.Context, parentID string) ([]domain.FileItem, error) {
	if d.files == nil {
		return nil, errNotInit()
	}
	parent := d.normalizePath(parentID)
	items := make([]domain.FileItem, 0, 64)
	token := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := d.files.List(ctx, &sdkUserFile.FileListRequest{
			Parent: &sdkUserFile.File{Path: parent},
			ListInfo: &sdkModel.ScanListRequest{
				Limit: listPageLimit,
				Token: token,
			},
		})
		if err != nil {
			return nil, mapError(err)
		}
		if resp == nil {
			break
		}
		for _, f := range resp.Files {
			if f == nil {
				continue
			}
			items = append(items, fileToItem(f))
		}
		if resp.ListInfo == nil || strings.TrimSpace(resp.ListInfo.Token) == "" {
			break
		}
		token = resp.ListInfo.Token
	}
	return items, nil
}

func (d *Driver) GetFileInfo(ctx context.Context, fileID string) (*domain.FileItem, error) {
	if d.files == nil {
		return nil, errNotInit()
	}
	path := d.normalizePath(fileID)
	if path == d.rootPath() {
		return &domain.FileItem{ID: path, Name: "根目录", IsDir: true, IDKind: domain.IDPath}, nil
	}
	f, err := d.files.Get(ctx, &sdkUserFile.File{Path: path})
	if err != nil {
		return nil, mapError(err)
	}
	item := fileToItem(f)
	return &item, nil
}

func (d *Driver) CreateFolder(ctx context.Context, parentID, name string) (*domain.FileItem, error) {
	if d.files == nil {
		return nil, errNotInit()
	}
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "/\\") {
		return nil, domain.Errorf(domain.CodeValidation, "文件夹名不合法：%q", name)
	}
	parent := d.normalizePath(parentID)
	// 首选带目录标记；个别服务端版本不认 dir 字段，回退成官方驱动的写法。
	created, err := d.files.Create(ctx, &sdkUserFile.File{Path: parent, Name: name, Dir: true})
	if err != nil {
		created, err = d.files.Create(ctx, &sdkUserFile.File{Path: parent, Name: name})
		if err != nil {
			return nil, mapError(err)
		}
	}
	item := domain.FileItem{
		ID:     d.childPath(parent, name),
		Name:   name,
		IsDir:  true,
		IDKind: domain.IDPath,
	}
	if created != nil && strings.TrimSpace(created.Path) != "" {
		item.ID = created.Path
	}
	return &item, nil
}

func (d *Driver) RenameFile(ctx context.Context, fileID, newName string) error {
	if d.files == nil {
		return errNotInit()
	}
	name := strings.TrimSpace(newName)
	if name == "" || strings.ContainsAny(name, "/\\") {
		return domain.Errorf(domain.CodeValidation, "新名称不合法：%q", newName)
	}
	path := d.normalizePath(fileID)
	if path == d.rootPath() {
		return domain.Errorf(domain.CodeValidation, "根目录不支持重命名")
	}
	if _, err := d.files.Rename(ctx, &sdkUserFile.File{Path: path, Name: name}); err != nil {
		return mapError(err)
	}
	return nil
}

func (d *Driver) MoveFiles(ctx context.Context, fileIDs []string, targetParentID, _ string) error {
	if d.files == nil {
		return errNotInit()
	}
	sources := d.sources(fileIDs)
	if len(sources) == 0 {
		return nil
	}
	dest := d.normalizePath(targetParentID)
	if _, err := d.files.Move(ctx, &sdkUserFile.BatchOperationRequest{
		Source: sources,
		Dest:   &sdkUserFile.File{Path: dest},
	}); err != nil {
		return mapError(err)
	}
	return nil
}

func (d *Driver) CopyFiles(ctx context.Context, fileIDs []string, targetParentID string) error {
	if d.files == nil {
		return errNotInit()
	}
	sources := d.sources(fileIDs)
	if len(sources) == 0 {
		return nil
	}
	dest := d.normalizePath(targetParentID)
	if _, err := d.files.Copy(ctx, &sdkUserFile.BatchOperationRequest{
		Source: sources,
		Dest:   &sdkUserFile.File{Path: dest},
	}); err != nil {
		return mapError(err)
	}
	return nil
}

// DeleteFiles 走 Trash（进 2dland 回收站），不是永久删除。
func (d *Driver) DeleteFiles(ctx context.Context, fileIDs []string) error {
	if d.files == nil {
		return errNotInit()
	}
	sources := d.sources(fileIDs)
	if len(sources) == 0 {
		return nil
	}
	if _, err := d.files.Trash(ctx, &sdkUserFile.BatchOperationRequest{Source: sources}); err != nil {
		return mapError(err)
	}
	return nil
}

// ResolveDownload 向 2dland 申请直链（过期时间由服务端下发）。
func (d *Driver) ResolveDownload(ctx context.Context, req driver.DownloadRequest) (*domain.DownloadInfo, error) {
	if d.files == nil {
		return nil, errNotInit()
	}
	path := d.normalizePath(req.FileID)
	resp, err := d.files.GetDirectDownloadAddress(ctx, &sdkUserFile.DirectDownloadRequest{Path: path})
	if err != nil {
		return nil, mapError(err)
	}
	if resp == nil || strings.TrimSpace(resp.DownloadAddress) == "" {
		return nil, domain.Errorf(domain.CodeDriverError, "2dland 未返回下载地址")
	}
	info := &domain.DownloadInfo{URL: resp.DownloadAddress, Mode: domain.DownloadRedirect}
	if resp.ExpireAt > 0 {
		if d := time.Until(time.UnixMilli(resp.ExpireAt)); d > 0 {
			info.Expiration = d
		}
	}
	if size := resp.FileSize; size > 0 {
		info.ChunkSize = size
	}
	return info, nil
}

func (d *Driver) sources(fileIDs []string) []*sdkUserFile.File {
	out := make([]*sdkUserFile.File, 0, len(fileIDs))
	for _, id := range fileIDs {
		path := d.normalizePath(id)
		if path == "" || path == d.rootPath() {
			continue
		}
		out = append(out, &sdkUserFile.File{Path: path})
	}
	return out
}
