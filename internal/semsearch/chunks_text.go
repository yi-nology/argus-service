package semsearch

import (
	"bytes"
	"strings"
)

// fileChunk 非 Go 文件的文件级 chunk：路径 + 首 fileChunkHeadLines 行。
func fileChunk(rel string, content []byte) Chunk {
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) > fileChunkHeadLines {
		lines = lines[:fileChunkHeadLines]
	}
	return Chunk{
		Path: rel,
		Line: 1,
		Text: rel + ":1\n" + clipText(strings.Join(lines, "\n")),
	}
}

// clipText embedding 输入截断（rune 安全；多字节腰斩会产生非法 UTF-8 进请求）。
func clipText(s string) string {
	const maxEmbedTextRunes = 2000
	runes := []rune(s)
	if len(runes) > maxEmbedTextRunes {
		return string(runes[:maxEmbedTextRunes])
	}
	return s
}

// itoa 避 fmt 的热路径小整数转字符串。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// isBinary 二进制嗅探：窗口内含 NUL 即跳过（与 codesearch 同口径）。
func isBinary(b []byte) bool {
	if len(b) > sniffLen {
		b = b[:sniffLen]
	}
	return bytes.IndexByte(b, 0) >= 0
}
