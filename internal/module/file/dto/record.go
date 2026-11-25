// Package dto 定义 file 模块的请求/响应类型。
//
// 分片协议 DTO 用类型别名指向 gouploader：字段与 binding 校验单一来源，
// 避免仓内副本与库协议漂移。文件记录响应直接使用 application.Record。
package dto

import "github.com/gtkit/gouploader"

type (
	ChunkInitRequest    = gouploader.ChunkInitRequest
	ChunkInitResponse   = gouploader.ChunkInitResponse
	ChunkUploadResponse = gouploader.ChunkUploadResponse
	ChunkStatusResponse = gouploader.ChunkStatusResponse
	ResumeRequest       = gouploader.ResumeRequest
	ResumeResponse      = gouploader.ResumeResponse
)

type ListRequest struct {
	Page     int `form:"page" binding:"omitempty,min=1,max=10000"`
	PageSize int `form:"page_size" binding:"omitempty,min=1,max=100"`
}

// ChunkForm 是分片上传的表单元数据。
type ChunkForm struct {
	UploadID   string `form:"upload_id" binding:"required"`
	ChunkIndex int    `form:"chunk_index" binding:"min=0"`
	Checksum   string `form:"checksum"`
}
