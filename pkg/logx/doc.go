// Package logx 提供应用内统一的日志接口层：自有 Logger 接口 + zap 实现，
// 所有日志写入本地日志文件（默认 JSON、lumberjack 轮转），终端保持干净；
// 并通过 ctx 注入在整个程序内传递 logger。
package logx
