package gui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"go-agent-framework/internal/apis"

	"github.com/egoist/mygo"
)

// 提取当前客户端的接口地址与凭据范围，用于配置变更时判断草稿文件是否需要重传
func uploadClientScope(c *apis.Client) uploadScope {
	if c == nil {
		return uploadScope{}
	}
	return uploadScope{
		endpoint: strings.TrimSpace(c.Endpoint),
		apiKey:   strings.TrimSpace(c.APIKey),
	}
}

// 弹出系统原生多选文件框，选中的文件统一走 Files API 后台预上传
// 原生弹窗为阻塞调用，放入后台 goroutine 执行并通过 updateUI 回到主线程交付结果
func (a *app) openFilePicker(sessionID int64) {
	deliver := func(paths []string, err error) {
		if err != nil {
			a.sessionState(sessionID).err = fmt.Errorf("选择附件失败: %w", err)
			return
		}
		if len(paths) > 0 {
			a.attachFiles(sessionID, paths)
		}
	}
	if a.pickFiles != nil {
		a.pickFiles(deliver)
		return
	}
	win := a.win
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:   win,
			Title:    "选择附件",
			Multiple: true,
		})
		a.updateUI(func() {
			deliver(paths, err)
		})
	}()
}

// 通过系统默认程序打开本地图片或文档副本，不阻塞 UI 主线程
func (a *app) openAttachmentPath(sessionID int64, path string) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return
	}
	if a.openPath != nil {
		if err := a.openPath(trimmed); err != nil {
			a.sessionState(sessionID).err = fmt.Errorf("打开附件失败: %w", err)
		}
		return
	}
	go func() {
		if err := mygo.Shell.OpenPath(trimmed); err != nil {
			a.updateUI(func() {
				a.sessionState(sessionID).err = fmt.Errorf("打开附件失败: %w", err)
			})
		}
	}()
}

// 校验并将选定/拖入的文件加入待发草稿队列，为每个合法文件启动独立预上传
// 混选含非法文件时保留合法项继续上传，并汇总错误展示到会话横幅
func (a *app) attachFiles(sessionID int64, paths []string) {
	state := a.sessionState(sessionID)
	if state.running {
		return
	}
	if strings.TrimSpace(a.cfg.APIKey) == "" {
		state.err = errors.New("请先在设置中配置 APIKey 后再上传附件")
		return
	}

	var errs []string
	added := false
	richLayout := state.docRichLayout
	for _, p := range paths {
		cleaned := strings.TrimSpace(p)
		name, size, spec, err := inspectAttachmentFile(cleaned, richLayout)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		a.nextAttachID++
		pf := &pendingFile{
			id:         a.nextAttachID,
			srcPath:    cleaned,
			name:       name,
			size:       size,
			purpose:    spec.Purpose,
			isDocument: spec.IsDocument,
			isImage:    spec.IsImage,
			status:     fileUploading,
		}
		state.addPendingFile(pf)
		a.startFileUpload(pf.id)
		added = true
	}
	if len(errs) > 0 {
		state.err = errors.New(strings.Join(errs, "；"))
	} else if added {
		state.err = nil
	}
}

// 按草稿附件 ID 跨会话查找待发文件
// 兼容新对话懒创建时草稿从 sessionID=0 迁移到正式会话 ID 的场景
func (a *app) findPendingFile(draftID int64) *pendingFile {
	for _, state := range a.sessionStates {
		for _, f := range state.pendingFiles {
			if f.id == draftID {
				return f
			}
		}
	}
	return nil
}

// 为单个附件启动独立后台 goroutine 预上传至 DeepSeek Files API
// 使用自增 uploadSeq 防护重试或切换 Key 时的陈旧回调覆盖，若期间附件已被移除则自动清理云端孤儿文件
func (a *app) startFileUpload(draftID int64) {
	file := a.findPendingFile(draftID)
	if file == nil {
		return
	}
	if file.cancel != nil {
		file.cancel()
		file.cancel = nil
	}
	a.syncClient()
	client := a.currentClient()

	baseCtx := a.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	uploadCtx, cancel := context.WithCancel(baseCtx)

	file.uploadSeq++
	seq := file.uploadSeq
	file.cancel = cancel
	file.status = fileUploading
	file.errMsg = ""
	file.fileID = ""
	file.expiresAt = 0
	file.uploadScope = uploadClientScope(client)

	req := apis.UploadFileRequest{
		Filename:            file.name,
		Purpose:             file.purpose,
		Path:                file.srcPath,
		ExpiresAfterSeconds: apis.DefaultFileExpiresSeconds,
	}

	go func() {
		defer cancel()
		uploaded, err := client.UploadFile(uploadCtx, req)
		a.updateUI(func() {
			cur := a.findPendingFile(draftID)
			if cur == nil || cur.uploadSeq != seq {
				if err == nil && uploaded.ID != "" {
					a.deleteRemoteFiles(client, uploaded.ID)
				}
				return
			}
			cur.cancel = nil
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				cur.status = fileFailed
				cur.errMsg = err.Error()
				return
			}
			cur.status = fileReady
			cur.fileID = uploaded.ID
			cur.expiresAt = uploaded.ExpiresAt
			if uploaded.Bytes > 0 {
				cur.size = uploaded.Bytes
			}
			cur.errMsg = ""
		})
	}()
}

// 按当前会话的文档解析模式重新校验源文件并触发后台预上传
func (a *app) retryPendingFile(sessionID, draftID int64) {
	state := a.sessionState(sessionID)
	if state.running {
		return
	}
	pf := a.findPendingFile(draftID)
	if pf == nil {
		return
	}
	if strings.TrimSpace(a.cfg.APIKey) == "" {
		pf.status = fileFailed
		pf.errMsg = "请先在设置中配置 APIKey 后再重试"
		return
	}
	name, size, spec, err := inspectAttachmentFile(pf.srcPath, state.docRichLayout)
	if err != nil {
		pf.status = fileFailed
		pf.errMsg = err.Error()
		return
	}
	pf.name = name
	pf.size = size
	pf.purpose = spec.Purpose
	pf.isDocument = spec.IsDocument
	pf.isImage = spec.IsImage
	a.startFileUpload(pf.id)
}

// 从草稿移除指定文件，立即取消进行中的上传并异步清理已生成的云端 file_id
func (a *app) removePendingFile(sessionID, draftID int64) {
	state := a.sessionState(sessionID)
	removed, ok := state.removePendingFile(draftID)
	if !ok {
		return
	}
	if removed.cancel != nil {
		removed.cancel()
		removed.cancel = nil
	}
	a.thumbs.evict(pathThumbKey(removed.srcPath, draftThumbMaxSide))
	if removed.fileID != "" {
		a.deleteRemoteFiles(a.currentClient(), removed.fileID)
	}
}

// 后台尽力删除不再需要的 DeepSeek 云端文件（去重且不阻塞 UI 主线程）
func (a *app) deleteRemoteFiles(client *apis.Client, ids ...string) {
	if client == nil || strings.TrimSpace(client.APIKey) == "" || len(ids) == 0 {
		return
	}
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; !ok {
			seen[trimmed] = struct{}{}
			unique = append(unique, trimmed)
		}
	}
	if len(unique) == 0 {
		return
	}
	go func() {
		for _, id := range unique {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = client.DeleteFile(ctx, id)
			cancel()
		}
	}()
}

// 配置切换后，将在旧 Endpoint/APIKey 下预上传的草稿文件清理并用新凭据重传
func (a *app) resyncPendingFilesForClient(prevClient *apis.Client) {
	newScope := uploadClientScope(a.currentClient())
	var staleIDs []string
	for _, state := range a.sessionStates {
		for _, pf := range state.pendingFiles {
			if pf.uploadScope == newScope {
				continue
			}
			if pf.cancel != nil {
				pf.cancel()
				pf.cancel = nil
			}
			if pf.fileID != "" {
				staleIDs = append(staleIDs, pf.fileID)
				pf.fileID = ""
				pf.expiresAt = 0
			}
			if strings.TrimSpace(a.cfg.APIKey) != "" {
				a.startFileUpload(pf.id)
			} else {
				pf.uploadSeq++
				pf.status = fileFailed
				pf.errMsg = "APIKey 已清空，请配置后重试"
			}
		}
	}
	if len(staleIDs) > 0 {
		a.deleteRemoteFiles(prevClient, staleIDs...)
	}
}

// 取消并清理指定会话中所有未发送草稿附件，返回待同步删除的云端 file_id 列表
func (a *app) cancelSessionDraftAttachments(state *sessionState) []string {
	var remoteIDs []string
	for _, p := range state.pendingImages {
		a.thumbs.evict(pendingThumbKey(p.id))
	}
	for _, f := range state.pendingFiles {
		if f.cancel != nil {
			f.cancel()
			f.cancel = nil
		}
		a.thumbs.evict(pathThumbKey(f.srcPath, draftThumbMaxSide))
		if f.fileID != "" {
			remoteIDs = append(remoteIDs, f.fileID)
		}
	}
	return remoteIDs
}

// 发送提交后清空当前会话的草稿图片、文件列表及对应缩略图缓存
func (a *app) clearSessionDraftAttachments(state *sessionState) {
	for _, p := range state.pendingImages {
		a.thumbs.evict(pendingThumbKey(p.id))
	}
	state.pendingImages = nil
	for _, pf := range state.pendingFiles {
		a.thumbs.evict(pathThumbKey(pf.srcPath, draftThumbMaxSide))
	}
	state.pendingFiles = nil
}

// 发送前检查草稿附件的云端有效期，将已过期项标为失败态并阻止本次提交
func markExpiredPendingFiles(files []*pendingFile) bool {
	nowUnix := time.Now().Unix()
	expired := false
	for _, pf := range files {
		if pf.expiresAt > 0 && nowUnix >= pf.expiresAt {
			pf.status = fileFailed
			pf.fileID = ""
			pf.errMsg = "云端文件已过期，请点击重试重新上传"
			expired = true
		}
	}
	return expired
}

// 发送前将草稿图片和预上传文件备份到本地附件目录；任意一步失败时回滚已写入文件
func (a *app) persistDraftAttachments(sessionID int64, state *sessionState) ([]apis.Image, []apis.File, error) {
	var savedImages []apis.Image
	if len(state.pendingImages) > 0 {
		savedImages = make([]apis.Image, 0, len(state.pendingImages))
		for _, p := range state.pendingImages {
			saved, err := a.store.SaveImage(a.ctx, sessionID, p.name, p.data)
			if err != nil {
				cleanupSavedAttachments(savedImages, nil)
				return nil, nil, fmt.Errorf("保存图片附件失败: %w", err)
			}
			savedImages = append(savedImages, saved)
		}
	}

	var savedFiles []apis.File
	if len(state.pendingFiles) > 0 {
		savedFiles = make([]apis.File, 0, len(state.pendingFiles))
		for _, pf := range state.pendingFiles {
			meta := apis.File{
				FileID:    pf.fileID,
				Name:      pf.name,
				Purpose:   pf.purpose,
				Size:      pf.size,
				ExpiresAt: pf.expiresAt,
			}
			saved, err := a.store.SaveFile(a.ctx, sessionID, meta, pf.srcPath, nil)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					savedFiles = append(savedFiles, meta)
					continue
				}
				cleanupSavedAttachments(savedImages, savedFiles)
				return nil, nil, fmt.Errorf("保存文件附件失败: %w", err)
			}
			savedFiles = append(savedFiles, saved)
		}
	}
	return savedImages, savedFiles, nil
}

// 发送前落盘或绑定会话失败时清理已写入的本地附件副本，避免残留孤儿文件
func cleanupSavedAttachments(images []apis.Image, files []apis.File) {
	for _, img := range images {
		if img.Path != "" {
			_ = os.Remove(img.Path)
		}
	}
	for _, f := range files {
		if f.Path != "" {
			_ = os.Remove(f.Path)
		}
	}
}

// 读取剪贴板复制的图片文件并加入内联 Base64 待发图片队列（受单消息总大小上限约束）
func (a *app) attachImageFiles(sessionID int64, paths []string) {
	state := a.sessionState(sessionID)
	if state.running {
		return
	}
	var errs []string
	added := false
	for _, p := range paths {
		name, mime, data, err := loadImageFile(p)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if state.pendingImageBytes()+int64(len(data)) > maxDraftImageTotalBytes {
			errs = append(errs, fmt.Sprintf("图片总大小超过单条消息上限（%d MiB）", maxDraftImageTotalBytes>>20))
			break
		}
		a.nextAttachID++
		state.addPendingImage(pendingImage{
			id:   a.nextAttachID,
			name: name,
			mime: mime,
			data: data,
		})
		added = true
	}
	if len(errs) > 0 {
		state.err = errors.New(strings.Join(errs, "；"))
	} else if added {
		state.err = nil
	}
}

// 校验剪贴板截图字节流并加入内联 Base64 待发图片队列
func (a *app) attachImageBytes(sessionID int64, name string, data []byte) {
	state := a.sessionState(sessionID)
	if state.running {
		return
	}
	mime, err := validateImageBytes(name, data)
	if err != nil {
		state.err = err
		return
	}
	if state.pendingImageBytes()+int64(len(data)) > maxDraftImageTotalBytes {
		state.err = fmt.Errorf("图片总大小超过单条消息上限（%d MiB）", maxDraftImageTotalBytes>>20)
		return
	}
	a.nextAttachID++
	state.addPendingImage(pendingImage{
		id:   a.nextAttachID,
		name: name,
		mime: mime,
		data: slices.Clone(data),
	})
	state.err = nil
}

// 移除剪贴板图片草稿并淘汰对应的缩略图缓存
func (a *app) removePendingImage(sessionID, imageID int64) {
	state := a.sessionState(sessionID)
	if state.removePendingImage(imageID) {
		a.thumbs.evict(pendingThumbKey(imageID))
	}
}
