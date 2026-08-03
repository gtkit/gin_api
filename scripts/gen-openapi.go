//go:build ignore

// gen-openapi 从路由描述符生成 api/openapi.json。
//
// 由 `make gen-openapi` 调用；`make check` 只做一致性比对，不重写文件——
// 门禁自动重写生成物等于取消门禁：任何漂移都会被"顺手修好"，没人会看到。
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/gtkit/gin-api/internal/appmeta"
	userhttp "github.com/gtkit/gin-api/internal/module/user/transport/http"
	"github.com/gtkit/gin-api/internal/openapi"
	"github.com/gtkit/gin-api/internal/router"
)

func main() {
	out := flag.String("out", "api/openapi.json", "output path")
	check := flag.Bool("check", false, "compare against the committed file instead of writing it")
	flag.Parse()

	rendered, err := render()
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate openapi: %v\n", err)
		os.Exit(1)
	}

	if *check {
		committed, readErr := os.ReadFile(*out)
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", *out, readErr)
			os.Exit(1)
		}
		if !bytes.Equal(bytes.TrimSpace(committed), bytes.TrimSpace(rendered)) {
			fmt.Fprintf(os.Stderr,
				"FAIL: %s is out of date with the route descriptors.\n"+
					"      Run `make gen-openapi` and commit the result.\n"+
					"      (The gate never rewrites the file itself — auto-fixing a drift gate\n"+
					"       means nobody ever sees the drift.)\n", *out)
			os.Exit(1)
		}
		fmt.Println("openapi contract is up to date")
		return
	}

	if err := os.WriteFile(*out, rendered, 0o644); err != nil { //nolint:gosec // 生成的契约文档需要可读
		fmt.Fprintf(os.Stderr, "write %s: %v\n", *out, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s\n", *out)
}

// render 走与生产完全相同的编译路径产出契约。
func render() ([]byte, error) {
	// 模块集合与 bootstrap 的 buildEngine 保持一致。
	// service 传 nil：契约只取描述符（方法/路径/策略/operationId），
	// 不执行任何 handler。
	modules := router.VersionedModules{
		V1: []router.Module{userhttp.NewModule(nil)},
	}

	routes, err := router.CompiledRoutesFor(modules)
	if err != nil {
		return nil, err
	}

	doc := openapi.Generate(routes, openapi.Options{
		Title:       appmeta.CommandName,
		Version:     "1.0.0",
		Description: "Public HTTP contract generated from the route descriptors.",
	})

	var buf bytes.Buffer
	if err := openapi.Write(&buf, doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
