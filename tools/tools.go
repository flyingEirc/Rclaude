//go:build tools

// Package tools 锁定 proto 代码生成插件版本，禁止业务代码引用。
package tools

import (
	_ "google.golang.org/grpc/cmd/protoc-gen-go-grpc"
	_ "google.golang.org/protobuf/cmd/protoc-gen-go"
)
