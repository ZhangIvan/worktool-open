package wire

import "testing"

// RFC 8785 附录 B 摘录案例（结构与本项目载荷同族：嵌套/数组/unicode/转义/数值）。
func TestCanonical(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"键排序", `{"b":1,"a":2}`, `{"a":2,"b":1}`},
		{"嵌套", `{"z":{"b":1,"a":2},"y":[3,1,2]}`, `{"y":[3,1,2],"z":{"a":2,"b":1}}`},
		{"数值规范化", `{"n1":1.0,"n2":1e2,"n3":-0,"n4":42}`, `{"n1":1,"n2":100,"n3":0,"n4":42}`},
		{"大数指数式", `{"big":1e21}`, `{"big":1e+21}`},
		{"小数指数式", `{"small":1e-7}`, `{"small":1e-7}`},
		{"字符串转义", `{"s":"a\"b\\c\nd\té"}`, `{"s":"a\"b\\c\nd\té"}`},
		{"控制字符转义", `{"k":"\u0007"}`, `{"k":"\u0007"}`},
		{"unicode 原样", `{"中文":"外部群测试群"}`, `{"中文":"外部群测试群"}`},
		{"空容器", `{ }`, `{}`},
		{"数组保序", `[3,1,2]`, `[3,1,2]`},
		{"null/bool", `{"a":null,"b":true,"c":false}`, `{"a":null,"b":true,"c":false}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Canonical([]byte(c.in))
			if err != nil {
				t.Fatalf("Canonical(%q) 报错: %v", c.in, err)
			}
			if string(got) != c.want {
				t.Fatalf("Canonical(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestCanonicalErrors(t *testing.T) {
	for _, bad := range []string{`{`, ``, `{"a":1} {"b":2}`, `trailing`} {
		if _, err := Canonical([]byte(bad)); err == nil {
			t.Fatalf("Canonical(%q) 应报错", bad)
		}
	}
}

// UTF-16 键序（RFC 8785 §3.2.3）：增补平面字符（U+1D5A0，代理对 0xD835 0xDDA0）
// 的首码元 0xD835 小于 BMP 私用区 U+E000，故 U+1D5A0 键排在前。
func TestCanonicalUTF16KeyOrder(t *testing.T) {
	in := "{\"\ue000\":1,\"\U0001D5A0\":2}"
	got, err := Canonical([]byte(in))
	if err != nil {
		t.Fatalf("Canonical err: %v", err)
	}
	want := "{\"\U0001D5A0\":2,\"\ue000\":1}"
	if string(got) != want {
		t.Fatalf("UTF-16 键序错误:\n got  %q\n want %q", got, want)
	}
}
