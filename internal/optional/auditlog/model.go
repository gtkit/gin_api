package auditlog

import "time"

// Record 列宽常量。构造点按它们截断字符串字段：
// 严格 sql_mode 下 varchar 溢出是整批写入失败，而审计走批量写，
// 一条超长记录会拖死同批的全部记录。
const (
	methodLen    = 10
	routeLen     = 255
	requestIDLen = 64
	clientIPLen  = 64
)

// Record 是一条写操作审计记录：谁、何时、对哪条路由、结果如何。
//
// 刻意不含请求体、响应体与 query string——它们是脱敏事故的既往来源，
// 访问日志已按各自受控策略覆盖。记录不可变，无软删除；
// 保留期清理见 Repository.DeleteOlderThan。
type Record struct {
	ID int64 `gorm:"column:id;primaryKey;autoIncrement;comment:主键编码" json:"id"`

	OccurredAt time.Time `gorm:"column:occurred_at;type:datetime;not null;index:idx_operation_audit_logs_occurred_at;index:idx_operation_audit_logs_user_occurred,priority:2;comment:发生时间"`
	Method     string    `gorm:"column:method;size:10;not null;comment:HTTP方法"`
	// Route 是路由模板（低基数，如 /api/v1/users/:id）；未匹配路由回退原始路径。
	Route     string `gorm:"column:route;size:255;not null;comment:路由模板"`
	Status    int    `gorm:"column:status;type:int;size:32;not null;comment:HTTP状态码"`
	LatencyMs int64  `gorm:"column:latency_ms;not null;comment:处理时延毫秒"`
	// UserID 未认证请求为 0。
	UserID    int64  `gorm:"column:user_id;not null;index:idx_operation_audit_logs_user_occurred,priority:1;comment:用户ID"`
	RequestID string `gorm:"column:request_id;size:64;not null;comment:请求ID"`
	ClientIP  string `gorm:"column:client_ip;size:64;not null;comment:客户端IP"`
}

// TableName 指定表名。
func (a *Record) TableName() string {
	return "operation_audit_logs"
}
