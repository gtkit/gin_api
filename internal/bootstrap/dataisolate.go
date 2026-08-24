package bootstrap

import (
	"fmt"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/pkg/dataisolate"
	"github.com/gtkit/gin-api/internal/platform/config"
	dao "github.com/gtkit/gin-api/internal/platform/data"
)

// installTenantIsolation 按配置在**服务运行时**的数据库句柄上安装租户隔离。
//
// 只在 dataisolate.enabled 为真时安装。与追踪埋点不同，这个插件装上就改变语句
// 行为，因此不能无条件挂——那等于替使用者做了一个改变数据可见性的决定。
//
// 刻意**不设**"启用了但没接中间件"的启动守卫：插件自身的 fail-closed 错误已经
// 写明缺什么（"把租户从已认证请求解析进上下文"），再加一层守卫只是把同一件事
// 说得更含糊，而且守卫必然先于第一个请求触发，反而挡住了那条更精确的信息。
//
// 迁移运行时（NewMigrationRuntime）不走这里，也不该走：migratex 读版本表时用
// `.Table(...)` 不带 Model，那类语句没有模型元数据，会撞上 fail-closed，
// 而失败信息会指向租户身份，与真实原因毫无关系。
func installTenantIsolation(cfg *config.Config, resources *dao.Resources) error {
	if cfg == nil || !cfg.DataIsolate.Enabled {
		return nil
	}

	opts := dataisolate.Options{
		Column: cfg.DataIsolate.Column,
		// 受管模型清单的单一来源。装配期用它核对判别列确实存在、
		// 豁免清单里的表名确实存在——两项在运行期都无法补救。
		Models:       models.Managed(),
		IgnoreTables: cfg.DataIsolate.IgnoreTables,
		ForceTables:  cfg.DataIsolate.Tables,
		AllowBypass:  cfg.DataIsolate.AllowBypass,
	}

	if err := resources.InstallTenantIsolation(opts); err != nil {
		return fmt.Errorf("tenant isolation: %w", err)
	}
	return nil
}
