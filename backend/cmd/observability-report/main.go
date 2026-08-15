// Command observability-report 汇总后端 JSONL 日志中的模型调用成本与耗时。
// 它只读取已有日志，不会启动服务、访问数据库或调用任何远程模型 API。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"rag-reasoning-platform/backend/internal/observability/baseline"
)

func main() {
	inputPath := flag.String("input", "-", "JSONL 日志路径；- 表示标准输入")
	outputPath := flag.String("output", "-", "JSON 报告路径；- 表示标准输出")
	flag.Parse()

	if err := run(*inputPath, *outputPath); err != nil {
		fmt.Fprintln(os.Stderr, "build observability report:", err)
		os.Exit(1)
	}
}

func run(inputPath string, outputPath string) error {
	input, closeInput, err := openInput(inputPath)
	if err != nil {
		return err
	}
	defer closeInput()

	report, err := baseline.Summarize(input, time.Now())
	if err != nil {
		return err
	}
	if inputPath == "-" {
		report.Source = "stdin"
	} else {
		report.Source = inputPath
	}

	output, closeOutput, err := openOutput(outputPath)
	if err != nil {
		return err
	}
	defer closeOutput()

	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return fmt.Errorf("encode baseline report: %w", err)
	}
	return nil
}

func openInput(path string) (io.Reader, func(), error) {
	if path == "-" {
		return os.Stdin, func() {}, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open input log: %w", err)
	}
	return file, func() { _ = file.Close() }, nil
}

func openOutput(path string) (io.Writer, func(), error) {
	if path == "-" {
		return os.Stdout, func() {}, nil
	}

	file, err := os.Create(path)
	if err != nil {
		return nil, func() {}, fmt.Errorf("create output report: %w", err)
	}
	return file, func() { _ = file.Close() }, nil
}
