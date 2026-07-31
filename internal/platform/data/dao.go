package dao

import (
	logger "github.com/gtkit/logger/v2"
	"github.com/gtkit/redisx"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

var (
	daoDB Dao
	_     Dao = (*dao)(nil)
)

type (
	MyDB = *gorm.DB
	// RDB 直接暴露 go-redis 原生客户端：业务层之前也是通过 .Client() 取它，
	// 换用 redisx 后由 dao 层统一解包，调用方少一层间接。
	RDB = *redis.Client
)

type Dao interface {
	Mdb() MyDB
	Rdb(db int) RDB
	MdbClose() error
	RdbClose() error
	d()
}

type dao struct {
	rdb *redisx.Client
	mdb MyDB
}

func (d *dao) d() {}

func Mdb() MyDB {
	return DB().Mdb()
}

func (d *dao) Mdb() MyDB {
	return d.mdb
}

func (d *dao) MdbClose() error {
	db, err := d.mdb.DB()
	if err != nil {
		return err
	}
	return db.Close()
}

func Rdb(db int) RDB {
	return DB().Rdb(db)
}

func RdbExec(db int, fn func(RDB) error) error {
	return fn(Rdb(db))
}

// Rdb 返回指定 DB 的 go-redis 客户端。
// 与旧版 redis.Select 一致：未初始化的 DB 返回 nil，由调用方判空。
func (d *dao) Rdb(db int) RDB {
	if d.rdb == nil {
		return nil
	}

	client, ok := d.rdb.GetClient(db)
	if !ok {
		return nil
	}
	return client
}

func (d *dao) RdbClose() error {
	if d.rdb == nil {
		return nil
	}
	return d.rdb.Close()
}

func DB() Dao {
	return daoDB
}

func DBClose() {
	if DB() == nil {
		return
	}

	if err := DB().MdbClose(); err != nil {
		logger.Error("[*]Mysql close error", zap.Error(err))
	}
	logger.Info("[*]Mysql close success")
	if err := DB().RdbClose(); err != nil {
		logger.Error("[*]Redis close error", zap.Error(err))
	}
	logger.Info("[*]Redis close success")
}
