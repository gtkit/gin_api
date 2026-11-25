// Package dto defines the HTTP file upload and metadata protocol.
package dto

type ChunkInitRequest struct {
	FileName  string `json:"file_name"            binding:"required"`
	FileSize  int64  `json:"file_size"            binding:"required,gt=0"`
	ChunkSize int64  `json:"chunk_size,omitempty"`
	Checksum  string `json:"checksum,omitempty"`
	FileHash  string `json:"file_hash,omitempty"`
	Algorithm string `json:"algorithm,omitempty"`
}

type ChunkInitResponse struct {
	UploadID    string `json:"upload_id"`
	ChunkSize   int64  `json:"chunk_size"`
	TotalChunks int    `json:"total_chunks"`

	Instant    bool   `json:"instant,omitempty"`
	StorageKey string `json:"storage_key,omitempty"`
	FileID     string `json:"file_id,omitempty"`
	URL        string `json:"url,omitempty"`

	UploadPath string `json:"upload_path,omitempty"`

	PartURLs      map[int]string `json:"part_urls,omitempty"`
	PartSignTTL   int            `json:"part_sign_ttl,omitempty"`
	RecommendConc int            `json:"recommend_concurrency,omitempty"`
}

type ChunkUploadResponse struct {
	UploadID      string `json:"upload_id"`
	ChunkIndex    int    `json:"chunk_index"`
	UploadedCount int    `json:"uploaded_count"`
	TotalChunks   int    `json:"total_chunks"`
}

type ChunkStatusResponse struct {
	UploadID       string `json:"upload_id"`
	TotalChunks    int    `json:"total_chunks"`
	UploadedChunks []int  `json:"uploaded_chunks"`
	MissingChunks  []int  `json:"missing_chunks"`
}

type ResumeRequest struct {
	FileHash  string `json:"file_hash"           binding:"required"`
	Algorithm string `json:"algorithm,omitempty"`
	FileName  string `json:"file_name,omitempty"`
	FileSize  int64  `json:"file_size,omitempty"`
}

type ResumeResponse struct {
	Instant    bool   `json:"instant,omitempty"`
	StorageKey string `json:"storage_key,omitempty"`
	FileID     string `json:"file_id,omitempty"`
	URL        string `json:"url,omitempty"`

	UploadID      string `json:"upload_id,omitempty"`
	UploadPath    string `json:"upload_path,omitempty"`
	TotalChunks   int    `json:"total_chunks,omitempty"`
	UploadedParts []int  `json:"uploaded_parts,omitempty"`
	MissingParts  []int  `json:"missing_parts,omitempty"`
}

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
