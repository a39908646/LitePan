package halalcloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdkUserFile "github.com/halalcloud/golang-sdk-lite/halalcloud/services/userfile"
	"github.com/ipfs/go-cid"

	"litepan/internal/domain"
	"litepan/internal/driver"
	"litepan/internal/driver/uploadutil"
)

// CID 参数默认值（服务端未下发时的兜底，与官方实现一致）。
const (
	defaultBlockCodec    = uint64(0x55)
	defaultBlockHashType = uint64(0x12)
)

// UploadLocalFile 实现 2dland 的分块上传：
//
//	① CreateUploadTask 建任务；返回 Created=true 表示服务端已有相同内容 → 秒传，零传输
//	② 按 BlockSize 切块，逐块 POST 到 {UploadAddress}/{Task}/{块CID}（先 GET 探测是否已存在）
//	③ makeFile 用全部块 CID 收尾，落库成正式文件
func (d *Driver) UploadLocalFile(ctx context.Context, req driver.LocalUploadRequest) (*driver.LocalUploadResult, error) {
	if d.files == nil {
		return nil, errNotInit()
	}
	name := strings.TrimSpace(req.FileName)
	if name == "" || strings.ContainsAny(name, "/\\") {
		return nil, domain.Errorf(domain.CodeValidation, "上传文件名不合法：%q", req.FileName)
	}
	src, err := os.Open(req.LocalPath)
	if err != nil {
		return nil, domain.Wrap(domain.CodeDriverError, err)
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return nil, domain.Wrap(domain.CodeDriverError, err)
	}
	size := st.Size()
	parent := d.normalizePath(req.ParentID)
	targetPath := d.childPath(parent, name)
	uploadutil.NotifyProgress(req.OnProgress, 0, size, "正在创建上传任务")

	task, err := d.files.CreateUploadTask(ctx, &sdkUserFile.File{Path: targetPath, Size: size})
	if err != nil {
		return nil, mapError(err)
	}
	if task == nil {
		return nil, domain.Errorf(domain.CodeDriverError, "2dland 未返回上传任务")
	}
	result := &driver.LocalUploadResult{
		FileID:   targetPath,
		ParentID: parent,
		FileName: name,
		Size:     size,
	}
	if task.Created {
		// Created=true 只说明服务端已有同名条目，不保证内容是对的：
		// 上一次 makeFile 失败会留下 0 字节占位，重试时服务端就回 Created=true。
		// 所以必须回读确认大小，不一致就继续走正常分块上传把它覆盖掉。
		if existing, err := d.files.Get(ctx, &sdkUserFile.File{Path: targetPath}); err == nil && existing != nil && existing.Size == size {
			uploadutil.NotifyProgress(req.OnProgress, size, size, "秒传成功")
			result.Message = "秒传成功（服务端已存在相同内容）"
			if strings.TrimSpace(existing.Path) != "" {
				result.FileID = existing.Path
			}
			return result, nil
		}
	}

	blockSize := task.BlockSize
	if blockSize <= 0 {
		blockSize = defaultBlockSize
	}
	codec := defaultBlockCodec
	if task.BlockCodec > 0 {
		codec = uint64(task.BlockCodec)
	}
	mhType := defaultBlockHashType
	if task.BlockHashType > 0 {
		mhType = uint64(task.BlockHashType)
	}
	prefix := cid.Prefix{Version: 1, Codec: codec, MhType: mhType, MhLength: -1}

	slices := make([]string, 0, size/blockSize+1)
	buf := make([]byte, blockSize)
	var uploaded int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := io.ReadFull(src, buf)
		if n > 0 {
			sliceCID, err := d.postSlice(ctx, task.UploadAddress, task.Task, prefix, buf[:n])
			if err != nil {
				return nil, err
			}
			slices = append(slices, sliceCID.String())
			uploaded += int64(n)
			uploadutil.NotifyProgress(req.OnProgress, uploaded, size, "正在上传到 2dland")
		}
		if readErr != nil {
			if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
				break
			}
			return nil, domain.Wrap(domain.CodeDriverError, readErr)
		}
	}
	if len(slices) == 0 {
		// 0 字节文件也要有一个空块，否则 makeFile 无内容可收尾。
		sliceCID, err := d.postSlice(ctx, task.UploadAddress, task.Task, prefix, nil)
		if err != nil {
			return nil, err
		}
		slices = append(slices, sliceCID.String())
	}
	created, err := d.makeFile(ctx, task.UploadAddress, task.Task, slices)
	if err != nil {
		return nil, err
	}
	if created != nil && strings.TrimSpace(created.Path) != "" {
		result.FileID = created.Path
	}
	uploadutil.NotifyProgress(req.OnProgress, size, size, "上传成功")
	result.Message = "上传成功"
	return result, nil
}

// postSlice 上传单个分块。先 GET 探测（服务端按 CID 去重），未命中再 POST 上传。
func (d *Driver) postSlice(ctx context.Context, uploadAddress, taskID string, prefix cid.Prefix, data []byte) (cid.Cid, error) {
	sliceCID, err := prefix.Sum(data)
	if err != nil {
		return cid.Undef, domain.Wrap(domain.CodeDriverError, err)
	}
	endpoint := fmt.Sprintf("%s/%s/%s", strings.TrimSuffix(uploadAddress, "/"), taskID, sliceCID.String())
	client := d.uploadHTTPClient()

	exists, err := d.probeSlice(ctx, client, endpoint)
	if err != nil {
		return cid.Undef, err
	}
	if exists {
		return sliceCID, nil
	}
	var lastErr error
	for attempt := 0; attempt < sliceRetryTimes; attempt++ {
		if err := ctx.Err(); err != nil {
			return cid.Undef, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
		if err != nil {
			return cid.Undef, domain.Wrap(domain.CodeDriverError, err)
		}
		req.ContentLength = int64(len(data))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("X-Content-Cid", sliceCID.String())
		req.Header.Set("X-Task-Id", taskID)
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
				return sliceCID, nil
			}
			lastErr = domain.Errorf(domain.CodeDriverError, "上传分块失败：%s %s", resp.Status, strings.TrimSpace(string(body)))
		} else {
			lastErr = domain.Wrap(domain.CodeDriverError, err)
		}
		time.Sleep(sliceRetryPeriod)
	}
	return cid.Undef, lastErr
}

// probeSlice 询问服务端该分块是否已存在（秒传/断点续传的基础）。
func (d *Driver) probeSlice(ctx context.Context, client *http.Client, endpoint string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, domain.Wrap(domain.CodeDriverError, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		// 探测失败不致命，交给后续 POST 判定。
		return false, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil
	}
	var hit bool
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err := json.Unmarshal(bytes.TrimSpace(body), &hit); err != nil {
		return false, nil
	}
	return hit, nil
}

// makeFileResult 是 makeFile 的响应。
//
// 不能直接反序列化到 sdkUserFile.File：SDK 把 size 声明成 json:",string"，
// 而该接口实际返回的是原始数字，会报 "cannot unmarshal number into ... size of type int64"。
type makeFileResult struct {
	Identity        string   `json:"identity"`
	Path            string   `json:"path"`
	Size            flexInt  `json:"size"`
	ContentIdentity string   `json:"content_identity"`
}

// flexInt 兼容 JSON 里数字与字符串两种编码。
type flexInt int64

func (f *flexInt) UnmarshalJSON(raw []byte) error {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text == "" || text == "null" {
		*f = 0
		return nil
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		// 允许小数形式（部分接口会给 float）
		floatValue, floatErr := strconv.ParseFloat(text, 64)
		if floatErr != nil {
			return err
		}
		value = int64(floatValue)
	}
	*f = flexInt(value)
	return nil
}

// makeFile 用分块 CID 列表收尾，生成正式文件。
func (d *Driver) makeFile(ctx context.Context, uploadAddress, taskID string, slices []string) (*makeFileResult, error) {
	endpoint := fmt.Sprintf("%s/%s", strings.TrimSuffix(uploadAddress, "/"), taskID)
	payload, err := json.Marshal(slices)
	if err != nil {
		return nil, domain.Wrap(domain.CodeDriverError, err)
	}
	client := d.uploadHTTPClient()
	var lastErr error
	for attempt := 0; attempt < sliceRetryTimes; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, domain.Wrap(domain.CodeDriverError, err)
		}
		req.ContentLength = int64(len(payload))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
				var out makeFileResult
				if err := json.Unmarshal(body, &out); err != nil {
					return nil, domain.Errorf(domain.CodeDriverError, "解析 makeFile 响应失败：%v（原始响应：%s）", err, truncateForError(body))
				}
				return &out, nil
			}
			lastErr = domain.Errorf(domain.CodeDriverError, "收尾生成文件失败：%s %s", resp.Status, strings.TrimSpace(string(body)))
		} else {
			lastErr = domain.Wrap(domain.CodeDriverError, err)
		}
		time.Sleep(sliceRetryPeriod)
	}
	return nil, lastErr
}

// truncateForError 截断响应体，避免把整个二进制塞进错误信息。
func truncateForError(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > 200 {
		return text[:200] + "..."
	}
	return text
}
