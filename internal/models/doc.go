// Package models 承载 GORM 持久化模型与跨层共享的值类型、枚举。
//
// 模型手写维护，每个类型只声明结构体与 TableName；业务规则放 application，
// 数据访问放 repository（依赖边界见 docs/architecture.md）。
package models
