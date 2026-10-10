package codeindex

import (
	"bytes"
	"compress/gzip"
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

//go:embed tree_ast.js
var treeParser string

//go:embed common_ast.js
var commonParser string

//go:embed COMMON-PARSER-LICENSES
var commonLicenses string

//go:embed tree-sitter*.gz
var treeBundles embed.FS

//go:embed TREE-SITTER-LICENSES
var treeLicenses string

func treeLanguage(language string) bool {
	switch language {
	case "java", "csharp", "rust", "cpp", "php", "c", "bash", "powershell", "html", "css", "graphql", "kotlin", "swift", "dart", "sql", "json", "yaml", "toml":
		return true
	}
	return false
}
func originalTreeLanguage(language string) bool {
	return language == "java" || language == "csharp" || language == "rust" || language == "cpp"
}
func treeParserScript(language string) string {
	if originalTreeLanguage(language) {
		return treeParser
	}
	return commonParser
}
func grammarName(language string) string {
	if language == "csharp" {
		return "c-sharp"
	}
	return language
}
func treeParserHash(language string) string {
	var data bytes.Buffer
	data.WriteString(treeParserScript(language))
	for _, name := range []string{"tree-sitter.js.gz", "tree-sitter.wasm.gz", "tree-sitter-" + grammarName(language) + ".wasm.gz"} {
		asset, _ := treeBundles.ReadFile(name) // Embedded names are fixed and covered by asset tests.
		data.Write(asset)
	}
	return digest(data.Bytes())
}
func treeAsset(name string) ([]byte, error) {
	compressed, err := treeBundles.ReadFile(name + ".gz")
	if err != nil {
		return nil, err
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, 8*1024*1024+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 8*1024*1024 {
		return nil, errors.New("AST asset exceeds bound")
	}
	return data, nil
}
func parseTrees(ctx context.Context, executable, language string, inputs []parserInput) (parserOutput, error) {
	if executable == "" {
		executable = "node"
	}
	path, err := exec.LookPath(executable)
	if err != nil {
		return parserOutput{}, errors.New("operator-owned Node.js is required for the selected Tree-sitter grammar; select --node")
	}
	runtime, err := treeAsset("tree-sitter.js")
	if err != nil {
		return parserOutput{}, err
	}
	wasm, err := treeAsset("tree-sitter.wasm")
	if err != nil {
		return parserOutput{}, err
	}
	grammar, err := treeAsset("tree-sitter-" + grammarName(language) + ".wasm")
	if err != nil {
		return parserOutput{}, err
	}
	data, err := json.Marshal(struct {
		Runtime  string        `json:"runtime"`
		WASM     string        `json:"wasm"`
		Grammar  string        `json:"grammar"`
		Language string        `json:"language"`
		Files    []parserInput `json:"files"`
	}{string(runtime), base64.StdEncoding.EncodeToString(wasm), base64.StdEncoding.EncodeToString(grammar), language, inputs})
	if err != nil {
		return parserOutput{}, err
	}
	output, err := runParser(ctx, path, []string{"--no-addons", "--no-global-search-paths", "--max-old-space-size=512", "--eval", treeParserScript(language)}, data)
	if err != nil {
		return parserOutput{}, fmt.Errorf("isolated %s AST parser failed: %w", language, err)
	}
	return decodeParser(output, inputs, "tree AST")
}
