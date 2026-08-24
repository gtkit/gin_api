package dao

import (
	"errors"
	"fmt"

	"github.com/gtkit/gin-api/internal/pkg/dataisolate"
)

// InstallTenantIsolation 给本进程的 GORM 连接安装共享表租户隔离插件。
//
// 与 InstrumentTracing 同一条理由挂在这里：连接句柄不外流，改动句柄行为的事
// 就该由句柄的属主做，而不是由拿不到句柄的层代做。
//
// 与追踪埋点的**关键差别**是它不无条件安装。追踪的开关只决定有没有导出，
// 埋点在不在无所谓；隔离的开关决定语句会不会被改写，装上就改变行为。
// 因此调用方按配置决定是否调用本方法，本方法本身不读配置。
//
// 装配面是一条边界：本插件只装在**服务运行时**的句柄上。迁移运行时
// （internal/bootstrap.NewMigrationRuntime）刻意不装——migratex 读版本表时用
// `.Table(...)` 不带 Model，那类语句没有模型元数据，会撞上本插件的 fail-closed，
// 而失败信息会指向租户身份，与真实原因（跑的是迁移路径）毫无关系。
// 既有的 otelgorm 插件同样只装在服务运行时。
//
// 任何一步失败都返回错误、不降级：隔离装不上而进程照常起来，是把
// "以为有隔离"当成了"有隔离"——那比启动失败危险得多。
func (r *Resources) InstallTenantIsolation(opts dataisolate.Options) error {
	if r == nil || r.mysql == nil {
		return errors.New("install tenant isolation: mysql connection is not initialized")
	}

	plugin, err := dataisolate.New(opts)
	if err != nil {
		return fmt.Errorf("install tenant isolation: %w", err)
	}
	// db.Use 会调用 plugin.Initialize，装配期校验在那里执行（它需要命名策略）。
	if err := r.mysql.DB().Use(plugin); err != nil {
		return fmt.Errorf("install tenant isolation: %w", err)
	}
	return nil
}
