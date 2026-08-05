// Package api 以编译期嵌入的方式携带对外契约。
//
// 嵌入而不是运行时读文件：部署产物只有二进制（镜像里没有源码树），
// 读文件的方案在容器里会直接 404，而那种缺失只有真去点开文档时才发现。
package api

import _ "embed"

// Contract 是构建时点的 OpenAPI 契约。
//
// 它与代码同源（gen-openapi 由路由描述符生成、一致性门禁保证与提交一致），
// 因此嵌入的这份不会与二进制的真实行为漂移。
//
//go:embed openapi.json
var Contract []byte
