// Package wire 实现 WorkTool-Open WS 帧编解码与逐帧鉴权原语
// （protocol/spec.md §4/§5 的 L2 服务端侧实现；任务卡 T2.3）。
//
// 本文件：RFC 8785（JSON Canonicalization Scheme, JCS）规范化 JSON。
// payloadDigest = SHA-256(JCS(list))（spec §5.2.2，冻结），金标准向量见
// protocol/fixtures/hmac-vector.json（L2 复算单测在 hmac_test.go，关 AUD-03 C-4）。
package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
)

// Canonical 解析 JSON 文本 data 并返回其 RFC 8785 规范化序列化。
// 数值按 ECMAScript Number::toString 语义重排（"1.0"→"1"、"1e2"→"100"），
// 对象键按 UTF-16 码元序排序，字符串按 JSON.stringify 转义。
// 已知边界：不支持字符串中的孤立代理项（lone surrogate）——合法 UTF-8 JSON 输入不会出现。
func Canonical(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("wire/jcs: JSON 解析失败: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("wire/jcs: 输入含多个 JSON 值")
	}
	return CanonicalValue(v)
}

// CanonicalValue 序列化已解码的 JSON 值（须由 UseNumber 解码器产出，
// 数值类型为 json.Number；float64 输入亦可处理）。
func CanonicalValue(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeJSONString(buf, x)
	case json.Number:
		s, err := canonicalNumber(x)
		if err != nil {
			return err
		}
		buf.WriteString(s)
	case float64:
		s, err := canonicalNumber(json.Number(strconv.FormatFloat(x, 'g', -1, 64)))
		if err != nil {
			return err
		}
		buf.WriteString(s)
	case int:
		buf.WriteString(strconv.Itoa(x))
	case int64:
		buf.WriteString(strconv.FormatInt(x, 10))
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return lessUTF16(keys[i], keys[j]) })
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSONString(buf, k)
			buf.WriteByte(':')
			if err := writeCanonical(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("wire/jcs: 不支持的值类型 %T（须为 UseNumber 解码产物）", v)
	}
	return nil
}

// lessUTF16 按 UTF-16 码元序列字典序比较（RFC 8785 §3.2.3）。
// ASCII 键退化为字节序比较；增补平面字符（代理对）按其 UTF-16 高/低代理码元排序。
func lessUTF16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	n := len(ua)
	if len(ub) < n {
		n = len(ub)
	}
	for i := 0; i < n; i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// writeJSONString 按 RFC 8785 §3.2.2.2 转义（与 JSON.stringify 一致）：
// 仅转义 `"`、`\` 与控制字符（\b\t\n\f\r 及 \u00xx 小写 hex），其余字符原样 UTF-8 输出。
func writeJSONString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
			} else if r == utf8.RuneError {
				// 字符串含无效 UTF-8（含被解码为 RuneError 的孤立代理项）：拒绝而非静默改写。
				buf.WriteByte('"') // 让上层报错时输出可辨认
				return
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}

// canonicalNumber 按 ECMAScript Number::toString 规范化数值字面量
// （RFC 8785 §3.2.2.3）：整数原样十进制；1e21 ≤ |x| 或 |x| < 1e-6 用指数式
// （尾数最短、指数无前导零、正指数带 +）；其余定点式最短表示。
func canonicalNumber(n json.Number) (string, error) {
	s := n.String()
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return strconv.FormatInt(i, 10), nil
	}
	f, err := n.Float64()
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return "", fmt.Errorf("wire/jcs: 数值 %q 不可表示为 IEEE-754 双精度", s)
	}
	if f == 0 {
		return "0", nil // 含 -0：JSON.stringify(-0) === "0"
	}
	abs := math.Abs(f)
	if abs >= 1e21 || abs < 1e-6 {
		e := strconv.FormatFloat(f, 'e', -1, 64) // 如 "1.2345e+21" / "1e-07"
		mant, exp, ok := cutLastByte(e, 'e')
		if !ok {
			return "", fmt.Errorf("wire/jcs: 指数式格式异常 %q", e)
		}
		expI, err := strconv.Atoi(exp)
		if err != nil {
			return "", fmt.Errorf("wire/jcs: 指数解析失败: %w", err)
		}
		sign := "+"
		if expI < 0 {
			sign = "-"
			expI = -expI
		}
		return mant + "e" + sign + strconv.Itoa(expI), nil
	}
	return strconv.FormatFloat(f, 'f', -1, 64), nil
}

func cutLastByte(s string, sep byte) (before, after string, found bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
