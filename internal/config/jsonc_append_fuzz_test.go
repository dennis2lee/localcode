package config

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Many commented files of shapes nobody wrote down, each given a key it
// does not have: the result has to parse, hold what the file held plus the
// key, and contain every byte of the original in its original order. The
// last is what "no comment moves" means: this writer only ever inserts.
//
// Seeded, so a failure names a file that can be made again.
func TestAKeyAddedToAnyCommentedFileKeepsEveryByte(t *testing.T) {
	for seed := int64(1); seed <= 1500; seed++ {
		r := rand.New(rand.NewSource(seed))
		src, members := randomCommentedConfig(r)
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		var before map[string]any
		if err := json.Unmarshal(stripComments([]byte(src)), &before); err != nil {
			t.Fatalf("seed %d: the generator wrote a file that does not parse: %v\n%s", seed, err, src)
		}

		// Sometimes a key the file has is changed in the same write.
		change := ""
		if len(members) > 0 && r.Intn(3) == 0 {
			change = members[r.Intn(len(members))]
		}
		err := updateRawConfig(path, func(raw map[string]json.RawMessage) error {
			raw["zz_added"] = json.RawMessage(`{"a":[1,2],"b":"x"}`)
			raw["aa_added"] = json.RawMessage(`true`)
			if change != "" {
				raw[change] = json.RawMessage(`"changed"`)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("seed %d: %v\n%s", seed, err, src)
		}
		raw, _ := os.ReadFile(path)
		out := string(raw)

		var after map[string]any
		if err := json.Unmarshal(stripComments(append([]byte(nil), raw...)), &after); err != nil {
			t.Fatalf("seed %d: the written file does not parse: %v\nbefore:\n%s\nafter:\n%s", seed, err, src, out)
		}
		want := map[string]any{}
		for k, v := range before {
			want[k] = v
		}
		want["zz_added"] = map[string]any{"a": []any{float64(1), float64(2)}, "b": "x"}
		want["aa_added"] = true
		if change != "" {
			want[change] = "changed"
		}
		if !reflect.DeepEqual(after, want) {
			t.Fatalf("seed %d: the written file holds something else\nbefore:\n%s\nafter:\n%s", seed, src, out)
		}
		if change == "" && !isSubsequence(src, out) {
			t.Fatalf("seed %d: bytes of the original are missing or out of order\nbefore:\n%q\nafter:\n%q", seed, src, out)
		}
		// Every comment is still there, as many times as it was.
		for i := 0; ; i++ {
			mark := fmt.Sprintf("c%d;", i)
			if !strings.Contains(src, mark) {
				break
			}
			if strings.Count(out, mark) != strings.Count(src, mark) {
				t.Fatalf("seed %d: comment %q is there %d times, was %d\nbefore:\n%s\nafter:\n%s", seed, mark, strings.Count(out, mark), strings.Count(src, mark), src, out)
			}
		}
		// The same line ends throughout, when the file had one kind.
		if strings.Contains(src, "\r\n") && strings.Count(out, "\n") != strings.Count(out, "\r\n") {
			t.Fatalf("seed %d: a file with Windows line ends came back with both kinds\n%q", seed, out)
		}

		// And the keys are now keys the file has: writing them again
		// changes nothing at all.
		if err := updateRawConfig(path, func(raw map[string]json.RawMessage) error {
			raw["zz_added"] = json.RawMessage(`{"a":[1,2],"b":"x"}`)
			raw["aa_added"] = json.RawMessage(`true`)
			return nil
		}); err != nil {
			t.Fatalf("seed %d, second write: %v\n%s", seed, err, out)
		}
		if again, _ := os.ReadFile(path); string(again) != out {
			t.Fatalf("seed %d: writing the same values again changed the file\nfirst:\n%q\nsecond:\n%q", seed, out, again)
		}
	}
}

// isSubsequence reports whether every byte of a appears in b in order.
func isSubsequence(a, b string) bool {
	i := 0
	for j := 0; j < len(b) && i < len(a); j++ {
		if a[i] == b[j] {
			i++
		}
	}
	return i == len(a)
}

// randomCommentedConfig writes a JSONC object with at least one comment
// in it, and answers with the names of its members.
func randomCommentedConfig(r *rand.Rand) (string, []string) {
	nl := "\n"
	if r.Intn(4) == 0 {
		nl = "\r\n"
	}
	indent := []string{"  ", "\t", "    ", ""}[r.Intn(4)]
	n := 0
	comment := func(force bool) string {
		if !force && r.Intn(3) != 0 {
			return ""
		}
		n++
		// Braces, commas, quotes and slashes inside, which are the
		// characters a scan could trip on.
		body := fmt.Sprintf("c%d; } , \" { : //", n-1)
		if r.Intn(2) == 0 {
			return " // " + body + nl
		}
		if r.Intn(3) == 0 {
			return " /* " + body + nl + indent + "more */ "
		}
		return " /* " + body + " */ "
	}
	space := func() string {
		return []string{"", " ", nl + indent, nl + nl + indent, "\t"}[r.Intn(5)]
	}
	value := func() string {
		return []string{
			`true`, `false`, `null`, `0`, `-12.5e3`, `""`, `"a//b"`, `"/* not a comment */"`, `"quote \" brace } comma ,"`,
			`[]`, `{}`, `[1, 2, {"x": "}"}]`, `{"k": {"deep": ["//", "*/"]}}`, `"http://127.0.0.1:9/v1"`,
		}[r.Intn(14)]
	}

	var b strings.Builder
	if r.Intn(4) == 0 {
		b.WriteString(strings.TrimLeft(comment(true), " "))
		if !strings.HasSuffix(b.String(), nl) {
			b.WriteString(nl)
		}
	}
	b.WriteString("{")
	count := r.Intn(5)
	var names []string
	for i := 0; i < count; i++ {
		b.WriteString(space())
		b.WriteString(comment(false))
		if strings.HasSuffix(b.String(), nl) {
			b.WriteString(indent)
		}
		name := fmt.Sprintf("k%d", i)
		names = append(names, name)
		fmt.Fprintf(&b, "%q%s:%s%s", name, []string{"", " "}[r.Intn(2)], []string{"", " "}[r.Intn(2)], value())
		last := i == count-1
		if !last || r.Intn(3) == 0 {
			if r.Intn(4) == 0 {
				b.WriteString(comment(false))
			}
			b.WriteString(",")
		}
		b.WriteString(comment(false))
	}
	if n == 0 {
		b.WriteString(comment(true))
	} else {
		b.WriteString(comment(false))
	}
	b.WriteString(space())
	b.WriteString("}")
	if r.Intn(2) == 0 {
		b.WriteString(nl)
	}
	if r.Intn(5) == 0 {
		b.WriteString("// c_after_the_end }" + nl)
	}
	return b.String(), names
}

// The other half of one write: every value the file has is replaced while
// two keys are added. The last member is always among the replaced, which
// is the member the new keys are written after, so its replacement and
// the comma that follows it land at one offset. Named shapes first (a key
// written twice, a key spelled with an escape, an object with nothing in
// it and comments on both sides), then the generated files above.
//
// From a review of the change that added the append; it found nothing,
// and is kept so the next change has to pass it too.
func TestEveryValueReplacedWhileKeysAreAdded(t *testing.T) {
	cases := []string{
		`/* before */ {} /* after */`,
		"// before\r\n{\r\n\t// inside\r\n}\r\n// after\r\n",
		`{"same": 1, /* duplicate */ "same": 2 /* tail */}`,
		"{\r\n\t\"same\": 1,\r\n\t\"same\": {\"nested\": [1, 2,]}, // tail\r\n}\r\n",
		`{"same": 1, "other": "} /*", "same": 2, /* tail */}`,
		`{"same": 1, "same": 2 /* end */}`,
		`{/* leading */ "same": {"nested": 1}, /* trailing */}`,
		"{\n\"same\": true // tail\n// second tail\n}",
	}
	for seed := int64(1); seed <= 1500; seed++ {
		src, _ := randomCommentedConfig(rand.New(rand.NewSource(seed)))
		cases = append(cases, src)
	}
	for i, src := range cases {
		var before map[string]json.RawMessage
		if err := json.Unmarshal(stripComments([]byte(src)), &before); err != nil {
			t.Fatalf("case %d: the input does not parse: %v\n%s", i, err, src)
		}
		after := make(map[string]json.RawMessage, len(before)+2)
		for key := range before {
			after[key] = json.RawMessage(`{"changed":[true,"x"]}`)
		}
		after["zz_added"] = json.RawMessage(`{"a":[1,2]}`)
		after["aa_added"] = json.RawMessage(`true`)
		out, ok := spliceKeys([]byte(src), before, after)
		if !ok {
			t.Fatalf("case %d was refused:\n%s", i, src)
		}
		var got, want map[string]any
		if err := json.Unmarshal(stripComments(append([]byte(nil), out...)), &got); err != nil {
			t.Fatalf("case %d: the written file does not parse: %v\nbefore:\n%s\nafter:\n%s", i, err, src, out)
		}
		encoded, _ := json.Marshal(after)
		if err := json.Unmarshal(encoded, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("case %d: the written file holds something else\nbefore:\n%s\nafter:\n%s", i, src, out)
		}
		if again, ok := spliceKeys(out, after, after); !ok || string(again) != string(out) {
			t.Fatalf("case %d: writing the same values again changed the file\nfirst:\n%q\nsecond:\n%q", i, out, again)
		}
	}
}
