// Package paging 提供与具体存储无关的纯分页类型（查询入参与结果元信息）。
//
// 本包不依赖 gorm，任何层（models / application / dto）都可安全 import；
// 真正执行 GORM 分页查询的执行器在 github.com/gtkit/ormx/paginator（依赖 gorm），
// 二者分离避免业务层因引用分页类型而被迫依赖基础设施包。
package paging

// Query 分页查询的中立入参（协议无关、不依赖 gorm）。
//
// Sort 是外部排序键，必须经数据访问层的白名单映射后才进入 SQL——
// 动态排序列进不了参数占位符，白名单是 ORM 项目里防注入的唯一手段
// （见 docs/db-patterns.md「防御性查询」）。Order 取 asc / desc。
type Query struct {
	Page     int
	PageSize int
	Sort     string
	Order    string
}

// Paging 分页结果元信息。
type Paging struct {
	CurrentPage int    `json:"currentPage"` // 当前页
	PageSize    int    `json:"pageSize"`    // 每页条数
	TotalPage   int    `json:"totalPage"`   // 总页数
	TotalCount  int64  `json:"totalCount"`  // 总条数
	NextPageURL string `json:"nextPageURL"` // 下一页的链接
	PrevPageURL string `json:"prevPageURL"` // 上一页的链接
}
