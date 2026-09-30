package fastjson

import (
	"strings"
	"testing"
	"unsafe"
)

// distinctBacking verifies that the detached string doesn't point into the
// same backing byte store as the original string. Empty strings share the
// runtime empty-string address legitimately, so they are skipped.
func assertDistinctBacking(t *testing.T, what, orig, detached string) {
	t.Helper()
	if len(orig) == 0 {
		return
	}
	p1 := (*stringHeader)(unsafe.Pointer(&orig)).Data
	p2 := (*stringHeader)(unsafe.Pointer(&detached)).Data
	if p1 == p2 {
		t.Fatalf("%s shares byte backing store with the original value", what)
	}
}

type stringHeader struct {
	Data unsafe.Pointer
	Len  int
}

func TestValueDetachNested(t *testing.T) {
	json := `{"z":1,"a":{"y":[1,{"x":"raw\nesc\t\"v"}],"n":null},"b":true}`

	var p Parser
	v, err := p.Parse(json)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	d := v.Detach()

	if got, want := d.String(), json; got != want {
		t.Fatalf("unexpected detached json;\ngot\n%s\nwant\n%s", got, want)
	}

	// Object key order is preserved at every level.
	t.Run("key order", func(t *testing.T) {
		var keys []string
		d.GetObject().Visit(func(k []byte, _ *Value) {
			keys = append(keys, string(k))
		})
		want := []string{"z", "a", "b"}
		if strings.Join(keys, ",") != strings.Join(want, ",") {
			t.Fatalf("unexpected key order; got %v; want %v", keys, want)
		}
	})

	// Raw escaped string survives detach verbatim, both via Get and
	// via raw marshal bytes.
	t.Run("escaped string", func(t *testing.T) {
		if got, want := string(d.GetStringBytes("a", "y", "1", "x")), "raw\nesc\t\"v"; got != want {
			t.Fatalf("unexpected unescaped string; got %q; want %q", got, want)
		}
		if got, want := d.Get("a", "y", "1", "x").String(), `"raw\nesc\t\"v"`; got != want {
			t.Fatalf("unexpected raw string json; got %q; want %q", got, want)
		}
	})

	// Nested null/bool remain the same JSON types.
	if d.Get("a", "n").Type() != TypeNull {
		t.Fatalf("unexpected type for nested null: %s", d.Get("a", "n").Type())
	}
	if !d.GetBool("b") {
		t.Fatalf("unexpected value for nested bool")
	}
}

func TestValueDetachSparseArray(t *testing.T) {
	// Sparse array: gaps are filled with null singletons during Set*.
	var a Arena
	v := a.NewArray()
	v.SetArrayItem(0, a.NewNumberInt(42))
	v.SetArrayItem(2, a.NewString("gap"))
	v.SetArrayItem(5, a.NewNull())

	d := v.Detach()
	want := `[42,null,"gap",null,null,null]`
	if got := d.String(); got != want {
		t.Fatalf("unexpected sparse array json; got %s; want %s", got, want)
	}

	// Every element, including null gaps, must be a distinct owned Value.
	arr := d.GetArray()
	for i := range arr {
		if arr[i] == nil {
			t.Fatalf("nil element at %d", i)
		}
	}
	if arr[1] == arr[3] || arr[3] == arr[5] || arr[1] == valueNull {
		t.Fatalf("sparse nulls share Value pointers or the global singleton")
	}

	// Filling a gap on the detached copy doesn't touch the original.
	d.SetArrayItem(1, MustParse(`7`))
	if got := v.String(); got != want {
		t.Fatalf("original sparse array mutated; got %s; want %s", got, want)
	}

	a.Reset()
	if got := d.String(); got != `[42,7,"gap",null,null,null]` {
		t.Fatalf("detached sparse array invalid after Arena.Reset: %s", got)
	}
}

func TestValueDetachHugeNumber(t *testing.T) {
	// Numbers beyond float64 precision must keep the exact literal form.
	lit := "1234567890123456789012345678901234567890.123456789e-50"
	var p Parser
	v, err := p.Parse(`{"big":` + lit + `}`)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	d := v.Detach()

	if got := d.Get("big").MarshalTo(nil); string(got) != lit {
		t.Fatalf("number literal drifted; got %s; want %s", got, lit)
	}

	// Reuse the parser's value and byte buffers many times.
	for range 20 {
		_, err := p.Parse(`{"big":"x","filler":"aaaaaaaaaaaaaaaaaaaa"}`)
		if err != nil {
			t.Fatalf("unexpected error on re-parse: %s", err)
		}
	}
	if got := d.Get("big").MarshalTo(nil); string(got) != lit {
		t.Fatalf("number literal corrupted after Parser.Parse reuse; got %s; want %s", got, lit)
	}
	assertDistinctBacking(t, "huge number literal", v.Get("big").s, d.Get("big").s)
}

func TestValueDetachSingletons(t *testing.T) {
	var p Parser
	v, err := p.Parse(`[null,true,false]`)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	d := v.Detach()

	wants := []Type{TypeNull, TypeTrue, TypeFalse}
	orig := v.GetArray()
	got := d.GetArray()
	for i := range wants {
		if got[i].Type() != wants[i] {
			t.Fatalf("unexpected type at %d: %s; want %s", i, got[i].Type(), wants[i])
		}
		if got[i] == orig[i] {
			t.Fatalf("singleton at %d shares the source Value pointer", i)
		}
		if got[i] == valueNull || got[i] == valueTrue || got[i] == valueFalse {
			t.Fatalf("singleton at %d points to the package-level Value", i)
		}
	}
	if d.String() != `[null,true,false]` {
		t.Fatalf("unexpected singleton json: %s", d.String())
	}
}

func TestValueDetachMutationsIsolated(t *testing.T) {
	json := `{"o":{"k1":"v1","k2":"v2"},"a":[10,20,30],"s":"keep"}`
	var p Parser
	v, err := p.Parse(json)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	d := v.Detach()

	// Mutate the detached subtree with Set/Del on objects, arrays and
	// nested paths; the original tree must stay byte-identical.
	d.Get("o").Del("k1")
	d.Get("o").Set("k3", MustParse(`"v3"`))
	d.Get("a").Del("1")
	d.Get("a").Set("0", MustParse(`99`))
	d.Set("s", MustParse(`"changed"`))

	if got := d.String(); got != `{"o":{"k2":"v2","k3":"v3"},"a":[99,30],"s":"changed"}` {
		t.Fatalf("unexpected detached json after mutation: %s", got)
	}
	if got := v.String(); got != json {
		t.Fatalf("original tree mutated by detached writes;\ngot\n%s\nwant\n%s", got, json)
	}

	// Mutate the original side too; the detached side is unaffected.
	v.Get("o").Del("k2")
	v.Get("a").Del("0")
	if got := d.String(); got != `{"o":{"k2":"v2","k3":"v3"},"a":[99,30],"s":"changed"}` {
		t.Fatalf("detached tree mutated by original writes: %s", got)
	}
}

func TestValueDetachEscapedKeyIndependence(t *testing.T) {
	// Key contains an escape sequence; unescaping it mutates Object state
	// and rewrites key strings in place.
	var p Parser
	v, err := p.Parse(`{"fo\no":1,"plain":2}`)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	d := v.Detach()

	// Force key unescaping on the detached object only.
	if d.Get("fo\no").GetInt() != 1 {
		t.Fatalf("cannot look up escaped key on detached object")
	}
	if !d.GetObject().keysUnescaped {
		t.Fatalf("detached object keys must be unescaped after Get")
	}
	if v.GetObject().keysUnescaped {
		t.Fatalf("unscaping detached keys leaked into the original object")
	}

	// Original keeps the raw key form; both serialize identically.
	if got := v.String(); got != `{"fo\no":1,"plain":2}` {
		t.Fatalf("original json changed: %s", got)
	}
	if got := d.String(); got != `{"fo\no":1,"plain":2}` {
		t.Fatalf("detached json changed: %s", got)
	}
	assertDistinctBacking(t, "escaped object key", v.o.kvs[0].k, d.o.kvs[0].k)
}

func TestValueDetachNodeIsolation(t *testing.T) {
	json := `{"s":"hello","n":42,"o":{"k":"v"},"a":[1,"x",null]}`
	var p Parser
	v, err := p.Parse(json)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	d := v.Detach()

	// No nested Value pointer is shared.
	if d == v {
		t.Fatalf("root Value is shared")
	}
	if &d.o == &v.o {
		t.Fatalf("root Object is shared")
	}

	var origPtrs, detachedPtrs map[*Value]bool
	collectValuePtrs(v, &origPtrs, 0)
	collectValuePtrs(d, &detachedPtrs, 0)
	for ptr := range detachedPtrs {
		if ptr == valueNull || ptr == valueTrue || ptr == valueFalse {
			t.Fatalf("detached tree contains package singleton %p", ptr)
		}
		if origPtrs[ptr] {
			t.Fatalf("detached tree shares Value pointer %p with the original", ptr)
		}
	}

	// Scalars don't share byte backing stores with Parser.b.
	assertDistinctBacking(t, "string value", v.Get("s").s, d.Get("s").s)
	assertDistinctBacking(t, "number value", v.Get("n").s, d.Get("n").s)
	assertDistinctBacking(t, "nested object key", v.Get("o").o.kvs[0].k, d.Get("o").o.kvs[0].k)
	assertDistinctBacking(t, "array string element", v.GetArray("a")[1].s, d.GetArray("a")[1].s)

	// kv and array backing slices are freshly allocated.
	if &v.o.kvs[0] == &d.o.kvs[0] {
		t.Fatalf("object kv backing array is shared")
	}
	if len(v.a)+len(v.GetArray("a")) > 0 {
		if &v.GetArray("a")[0] == &d.GetArray("a")[0] {
			t.Fatalf("array backing slice is shared")
		}
	}

	// Growing the detached Object kvs slice cannot clobber the original.
	for range 16 {
		d.Get("o").Set("extra-key", MustParse(`0`))
	}
	if got := v.GetObject("o").Len(); got != 1 {
		t.Fatalf("original nested object grew after detached Set: len=%d", got)
	}
}

func collectValuePtrs(v *Value, ptrs *map[*Value]bool, depth int) {
	if v == nil || depth > MaxDepth+2 {
		return
	}
	if *ptrs == nil {
		*ptrs = make(map[*Value]bool)
	}
	(*ptrs)[v] = true
	switch v.t {
	case TypeObject:
		for i := range v.o.kvs {
			collectValuePtrs(v.o.kvs[i].v, ptrs, depth+1)
		}
	case TypeArray:
		for _, vv := range v.a {
			collectValuePtrs(vv, ptrs, depth+1)
		}
	}
}

func TestValueDetachParserPoolReuse(t *testing.T) {
	json := `{"keep":{"arr":[1,{"k":"v\tx"},"big",null,true]},"drop":"x"}`
	var pp ParserPool

	p := pp.Get()
	v, err := p.Parse(json)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	d := v.Get("keep").Detach()
	pp.Put(p)

	// Repeated pool churn: the same backing Values/bytes may be handed out
	// and overwritten with unrelated JSON many times.
	for i := range 20 {
		pp2 := pp.Get()
		_, err := pp2.Parse(`{"keep":"OVERWRITTEN","arr":[{"k":"zzzzzzzzzz"}]}`)
		if err != nil {
			t.Fatalf("unexpected error on iteration %d: %s", i, err)
		}
		pp.Put(pp2)
	}

	want := `{"arr":[1,{"k":"v\tx"},"big",null,true]}`
	if got := d.String(); got != want {
		t.Fatalf("detached subtree corrupted after pool reuse;\ngot\n%s\nwant\n%s", got, want)
	}
	// The detached subtree stays mutable.
	d.Set("new", MustParse(`[1,2]`))
	d.Get("arr").Del("3")
	if got := d.String(); got != `{"arr":[1,{"k":"v\tx"},"big",true],"new":[1,2]}` {
		t.Fatalf("unexpected detached json after post-reuse mutation: %s", got)
	}
}

func TestValueDetachArenaReset(t *testing.T) {
	var a Arena
	o := a.NewObject()
	o.Set("s", a.NewString(`quote"nl
nl`))
	o.Set("n", a.NewNumberFloat64(2.5))
	sub := a.NewObject()
	sub.Set("k", a.NewStringBytes([]byte("bytes")))
	arr := a.NewArray()
	arr.SetArrayItem(1, a.NewString("gap"))
	o.Set("sub", sub)
	o.Set("arr", arr)

	d := o.Detach()
	a.Reset()

	want := `{"s":"quote\"nl\nnl","n":2.5,"sub":{"k":"bytes"},"arr":[null,"gap"]}`
	if got := d.String(); got != want {
		t.Fatalf("detached arena value invalid after Reset;\ngot\n%s\nwant\n%s", got, want)
	}

	// A detached value survives repeated arena reuse cycles.
	for range 5 {
		o2 := a.NewObject()
		o2.Set("s", a.NewString("overwrite"))
		o2.Set("n", a.NewNumberInt(1))
		a.Reset()
	}
	if got := d.String(); got != want {
		t.Fatalf("detached arena value corrupted by arena reuse;\ngot\n%s\nwant\n%s", got, want)
	}
}

func TestValueDetachScanner(t *testing.T) {
	var sc Scanner
	sc.Init(`{"first":[1,2,3]} {"second":[4,5,6]}`)
	if !sc.Next() {
		t.Fatalf("unexpected scanner error: %s", sc.Error())
	}
	first := sc.Value().Detach()

	if !sc.Next() {
		t.Fatalf("unexpected scanner error on second value: %s", sc.Error())
	}
	// Reading the next value resets the scanner cache and reuses Values.

	if got := first.String(); got != `{"first":[1,2,3]}` {
		t.Fatalf("detached scanner value invalid after Next: %s", got)
	}
	first.Get("first").Set("1", MustParse(`99`))
	if got := first.String(); got != `{"first":[1,99,3]}` {
		t.Fatalf("detached scanner value not mutable: %s", got)
	}
}

func TestValueDetachRootScalars(t *testing.T) {
	cases := []struct {
		json string
		typ  Type
	}{
		{`"raw\nstr"`, typeRawString},
		{`123456789012345678901234567890`, TypeNumber},
		{`-0.25e3`, TypeNumber},
		{`null`, TypeNull},
		{`true`, TypeTrue},
		{`false`, TypeFalse},
		{`[]`, TypeArray},
		{`{}`, TypeObject},
	}
	for _, cc := range cases {
		var p Parser
		v, err := p.Parse(cc.json)
		if err != nil {
			t.Fatalf("unexpected error for %q: %s", cc.json, err)
		}
		d := v.Detach()
		if d.t != cc.typ {
			t.Fatalf("unexpected detached type for %q: %d; want %d", cc.json, d.t, cc.typ)
		}
		if got := d.String(); got != cc.json {
			t.Fatalf("unexpected detached json for %q: %s", cc.json, got)
		}
		// Reuse the parser; detached scalar must survive.
		if _, err := p.Parse(`"OOOOOOOOOOOOOOOOOOOO"`); err != nil {
			t.Fatalf("unexpected re-parse error: %s", err)
		}
		if got := d.String(); got != cc.json {
			t.Fatalf("detached root scalar %q corrupted after re-parse: %s", cc.json, got)
		}
		if d == v {
			t.Fatalf("detached root scalar %q shares Value with source", cc.json)
		}
	}
}

func TestValueDetachTypeStringBacking(t *testing.T) {
	var p Parser
	v, err := p.Parse(`{"s":"plain unescaped value"}`)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if v.Get("s").Type() != TypeString {
		t.Fatalf("unexpected type")
	}
	d := v.Detach()
	if d.Get("s").t != TypeString {
		t.Fatalf("detached value didn't preserve TypeString state: %d", d.Get("s").t)
	}
	assertDistinctBacking(t, "unescaped TypeString", v.Get("s").s, d.Get("s").s)
	if got := d.GetStringBytes("s"); string(got) != "plain unescaped value" {
		t.Fatalf("unexpected detached string: %q", got)
	}
}

func TestValueDetachNil(t *testing.T) {
	var v *Value
	if d := v.Detach(); d != nil {
		t.Fatalf("Detach on nil must return nil; got %v", d)
	}
}

func TestValueDetachDoesNotChangeOriginal(t *testing.T) {
	json := `{"b":[true,false,null],"o":{"x":1},"s":"a\nb"}`
	var p Parser
	v, err := p.Parse(json)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	_ = v.Detach()
	if got := v.String(); got != json {
		t.Fatalf("Detach mutated the original value;\ngot\n%s\nwant\n%s", got, json)
	}
	// keysUnescaped flags must not be toggled by Detach.
	if v.GetObject().keysUnescaped || v.Get("o").o.keysUnescaped {
		t.Fatalf("Detach toggled keysUnescaped on the original object")
	}
}
