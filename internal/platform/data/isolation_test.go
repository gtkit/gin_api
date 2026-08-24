package dao

import (
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/pkg/dataisolate"
)

// scopedProbe 是一个声明了判别列的模型，用于验证安装机制本身。
//
// 不用 models.Managed()：母版的模型都还没有判别列（多租户不是母版的默认形态），
// 拿它们验安装机制会撞上"没有模型声明该列"的装配期校验——那条校验是对的，
// 见 TestEnablingIsolationBeforeTheColumnExistsFails。
type scopedProbe struct {
	ID       int64  `gorm:"column:id;primaryKey"`
	TenantID string `gorm:"column:tenant_id"`
}

func (scopedProbe) TableName() string { return "scoped_probes" }

// isolationOptions 是一组能通过装配期校验的参数。
func isolationOptions() dataisolate.Options {
	return dataisolate.Options{Column: "tenant_id", Models: []any{&scopedProbe{}}}
}

// TestInstallTenantIsolationRegistersThePlugin 钉住插件真的装上了。
//
// 断言 db.Config.Plugins 而不是"没报错"：db.Use 的错误路径与"装了但没生效"
// 是两件事，而后者的表现是隔离静默缺席——正是本能力最坏的失效模式。
func TestInstallTenantIsolationRegistersThePlugin(t *testing.T) {
	t.Parallel()

	client, _ := newSQLMockOrmxClient(t)
	res := &Resources{mysql: client}

	before := len(client.DB().Plugins)
	if err := res.InstallTenantIsolation(isolationOptions()); err != nil {
		t.Fatalf("InstallTenantIsolation: %v", err)
	}

	plugins := client.DB().Plugins
	if len(plugins) != before+1 {
		t.Fatalf("plugin count = %d, want %d", len(plugins), before+1)
	}
	found := false
	for name := range plugins {
		if strings.Contains(name, "tenant-isolation") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no tenant isolation plugin among %v", plugins)
	}
}

// TestInstallTenantIsolationSurfacesAssemblyErrors 钉住装配期校验失败会上抛。
//
// 列名写错时判定永不命中，零张表受控，隔离静默消失。它必须在这里就失败，
// 而不是让进程带着一个什么都不做的插件正常起来。
func TestInstallTenantIsolationSurfacesAssemblyErrors(t *testing.T) {
	t.Parallel()

	client, _ := newSQLMockOrmxClient(t)
	res := &Resources{mysql: client}

	opts := isolationOptions()
	opts.Column = "tenant_i" // 写错一个字符
	err := res.InstallTenantIsolation(opts)

	if err == nil {
		t.Fatal("a column name that matches no model was accepted")
	}
	if !strings.Contains(err.Error(), "no managed model declares column") {
		t.Fatalf("error = %q, want it to name the unmatched column", err)
	}
}

// TestInstallTenantIsolationRequiresAConnection 钉住未初始化连接时报错而非 panic。
func TestInstallTenantIsolationRequiresAConnection(t *testing.T) {
	t.Parallel()

	if err := (&Resources{}).InstallTenantIsolation(isolationOptions()); err == nil {
		t.Fatal("installing on an uninitialised connection was accepted")
	}
}

// TestEnablingIsolationBeforeTheColumnExistsFails 钉住母版当前的真实状态。
//
// 本仓的受管模型都还没有判别列——多租户不是母版的默认形态。因此在加列之前
// 打开开关必须失败，且失败信息要指向"列名对不上模型"，因为那正是下一步该做的事
// （先给受控表加判别列并让模型声明它）。
//
// 这条判据也是那条启用顺序的可执行版本：顺序反了不会表现为"隔离没生效"，
// 而是进程起不来。等到有模型真的声明了该列，本用例会开始失败——那时它该被
// 改成"安装成功"，而这个失败本身就是提示。
func TestEnablingIsolationBeforeTheColumnExistsFails(t *testing.T) {
	t.Parallel()

	client, _ := newSQLMockOrmxClient(t)
	res := &Resources{mysql: client}

	err := res.InstallTenantIsolation(dataisolate.Options{
		Column: "tenant_id",
		Models: models.Managed(),
	})
	if err == nil {
		t.Fatal("isolation installed even though no managed model declares the discriminator column; " +
			"if a model now declares it, update this test to assert a successful install")
	}
	if !strings.Contains(err.Error(), "no managed model declares column") {
		t.Fatalf("error = %q, want it to point at the missing column", err)
	}
}
