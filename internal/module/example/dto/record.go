package dto

type WriteRequest struct {
	Title string `json:"title" binding:"required,max=120"`
}
type DeleteManyRequest struct {
	IDs []string `json:"ids" binding:"required,min=1,max=100,dive,required"`
}
type ListRequest struct {
	Page     int `form:"page" binding:"omitempty,min=1,max=10000"`
	PageSize int `form:"page_size" binding:"omitempty,min=1,max=100"`
}
