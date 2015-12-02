package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	C "github.com/glycerine/go-capnproto"
	"github.com/tpukep/bambam/bam"
	"github.com/tpukep/caps"
)

var (
	fprintf = fmt.Fprintf
	sprintf = fmt.Sprintf
	title   = strings.Title
)

var g_nodes = make(map[uint64]*node)
var g_imported map[string]bool
var g_segment *C.Segment

const GO_CAPNP_IMPORT = "github.com/glycerine/go-capnproto"

type node struct {
	Node
	pkg    string
	imp    string
	nodes  []*node
	name   string
	codecs map[uint64]bool
}

func assert(chk bool, format string, a ...interface{}) {
	if !chk {
		panic(sprintf(format, a...))
		os.Exit(1)
	}
}

func copyData(obj C.Object) int {
	r, off, err := g_segment.NewRoot()
	assert(err == nil, "%v\n", err)
	err = r.Set(0, obj)
	assert(err == nil, "%v\n", err)
	return off
}

func findNode(id uint64) *node {
	n := g_nodes[id]
	assert(n != nil, "could not find node 0x%x\n", id)
	return n
}

func (n *node) remoteScope(from *node) string {
	assert(n.pkg != "", "missing package declaration for %s", n.DisplayName())

	if n.imp == from.imp {
		return ""
	} else {
		assert(n.imp != "", "missing import declaration for %s", n.DisplayName())

		g_imported[n.imp] = true
		return n.pkg + "."
	}
}

func (n *node) remoteName(from *node) string {
	return n.remoteScope(from) + n.name
}

func (n *node) resolveName(base, name string, file *node) {
	if na := nameAnnotation(n.Annotations()); na != "" {
		name = na
	}
	if base != "" {
		n.name = base + title(name)
	} else {
		n.name = title(name)
	}

	n.pkg = file.pkg
	n.imp = file.imp

	if n.Which() != NODE_STRUCT || !n.Struct().IsGroup() {
		file.nodes = append(file.nodes, n)
	}

	for _, nn := range n.NestedNodes().ToArray() {
		if ni := g_nodes[nn.Id()]; ni != nil {
			ni.resolveName(n.name, nn.Name(), file)
		}
	}

	if n.Which() == NODE_STRUCT {
		for _, f := range n.Struct().Fields().ToArray() {
			if f.Which() == FIELD_GROUP {
				gname := f.Name()
				if na := nameAnnotation(f.Annotations()); na != "" {
					gname = na
				}
				findNode(f.Group().TypeId()).resolveName(n.name, gname, file)
			}
		}
	}
}

func nameAnnotation(annotations Annotation_List) string {
	for _, a := range annotations.ToArray() {
		if a.Id() == C.Name {
			if name := a.Value().Text(); name != "" {
				return name
			}
		}
	}
	return ""
}

type enumval struct {
	Enumerant
	val    int
	name   string
	tag    string
	parent *node
}

func (e *enumval) fullName() string {
	return fmt.Sprintf("%s_%s", strings.ToUpper(e.parent.name), strings.ToUpper(e.name))
}

func (n *node) defineEnum(w io.Writer) {
	for _, a := range n.Annotations().ToArray() {
		if a.Id() == C.Doc {
			fprintf(w, "// %s\n", a.Value().Text())
		}
	}
	fprintf(w, "type %s uint16\n", n.name)

	if es := n.Enum().Enumerants(); es.Len() > 0 {
		fprintf(w, "const (\n")

		ev := make([]enumval, es.Len())
		for i := 0; i < es.Len(); i++ {
			e := es.At(i)
			ename := e.Name()
			if an := nameAnnotation(e.Annotations()); an != "" {
				ename = an
			}

			t := ename
			for _, an := range e.Annotations().ToArray() {
				if an.Id() == C.Tag {
					t = an.Value().Text()
				} else if an.Id() == C.Notag {
					t = ""
				}
			}
			ev[e.CodeOrder()] = enumval{e, i, ename, t, n}
		}

		// not an iota, so type has to go on each line
		for _, e := range ev {
			fprintf(w, "%s %s = %d\n", e.fullName(), n.name, e.val)
		}

		fprintf(w, ")\n")

		fprintf(w, "func (c %s) String() string {\n", n.name)
		fprintf(w, "switch c {\n")
		for _, e := range ev {
			if e.tag != "" {
				fprintf(w, "case %s: return \"%s\"\n", e.fullName(), e.tag)
			}
		}
		fprintf(w, "default: return \"\"\n")
		fprintf(w, "}\n}\n\n")

		fprintf(w, "func %sFromString(c string) %s {\n", n.name, n.name)
		fprintf(w, "switch c {\n")
		for _, e := range ev {
			if e.tag != "" {
				fprintf(w, "case \"%s\": return %s\n", e.tag, e.fullName())
			}
		}
		fprintf(w, "default: return 0\n")
		fprintf(w, "}\n}\n")
	}
}

func (n *node) writeValue(w io.Writer, t Type, v Value) {
	switch t.Which() {
	case TYPE_VOID, TYPE_INTERFACE:
		fprintf(w, "struct{}")

	case TYPE_BOOL:
		assert(v.Which() == VALUE_BOOL, "expected bool value")
		if v.Bool() {
			fprintf(w, "true")
		} else {
			fprintf(w, "false")
		}

	case TYPE_INT8:
		assert(v.Which() == VALUE_INT8, "expected int8 value")
		fprintf(w, "int8(%d)", v.Int8())

	case TYPE_UINT8:
		assert(v.Which() == VALUE_UINT8, "expected uint8 value")
		fprintf(w, "uint8(%d)", v.Uint8())

	case TYPE_INT16:
		assert(v.Which() == VALUE_INT16, "expected int16 value")
		fprintf(w, "int16(%d)", v.Int16())

	case TYPE_UINT16:
		assert(v.Which() == VALUE_UINT16, "expected uint16 value")
		fprintf(w, "uint16(%d)", v.Uint16())

	case TYPE_INT32:
		assert(v.Which() == VALUE_INT32, "expected int32 value")
		fprintf(w, "int32(%d)", v.Int32())

	case TYPE_UINT32:
		assert(v.Which() == VALUE_UINT32, "expected uint32 value")
		fprintf(w, "uint32(%d)", v.Uint32())

	case TYPE_INT64:
		assert(v.Which() == VALUE_INT64, "expected int64 value")
		fprintf(w, "int64(%d)", v.Int64())

	case TYPE_UINT64:
		assert(v.Which() == VALUE_UINT64, "expected uint64 value")
		fprintf(w, "uint64(%d)", v.Uint64())

	case TYPE_FLOAT32:
		assert(v.Which() == VALUE_FLOAT32, "expected float32 value")
		fprintf(w, "float32(%f)", v.Float32())

	case TYPE_FLOAT64:
		assert(v.Which() == VALUE_FLOAT64, "expected float64 value")
		fprintf(w, "float64(%f)", v.Float64())

	case TYPE_TEXT:
		assert(v.Which() == VALUE_TEXT, "expected text value"+" got "+strconv.Itoa(int(v.Which())))
		fprintf(w, "%s", strconv.Quote(v.Text()))

	case TYPE_DATA:
		assert(v.Which() == VALUE_DATA, "expected data value")
		fprintf(w, "[]byte{")
		for i, b := range v.Data() {
			if i > 0 {
				fprintf(w, ", ")
			}
			fprintf(w, "%d", b)
		}
		fprintf(w, "}")

	case TYPE_ENUM:
		assert(v.Which() == VALUE_ENUM, "expected enum value")
		en := findNode(t.Enum().TypeId())
		assert(en.Which() == NODE_ENUM, "expected enum type ID")
		ev := en.Enum().Enumerants()
		if val := int(v.Enum()); val >= ev.Len() {
			fprintf(w, "%s(%d)", en.remoteName(n), val)
		} else {
			fprintf(w, "%s%s", en.remoteScope(n), ev.At(val).Name())
		}

	case TYPE_STRUCT:
		fprintf(w, "%s{ ", findNode(t.Struct().TypeId()).name)

		for _, f := range findNode(t.Struct().TypeId()).codeOrderFields() {
			fprintf(w, "%s: ", title(f.Name()))

			switch slot := f.Slot(); slot.Type().Which() {
			case TYPE_UINT8:
				val := v.Struct().ToStruct().Get8(int(slot.Offset()))
				fprintf(w, "uint8(%d),", uint8(val))

			case TYPE_UINT16:
				val := v.Struct().ToStruct().Get16(int(slot.Offset()))
				fprintf(w, "uint16(%d),", uint16(val))

			case TYPE_UINT32:
				val := v.Struct().ToStruct().Get32(int(slot.Offset()))
				fprintf(w, "uint32(%d),", uint32(val))

			case TYPE_UINT64:
				val := v.Struct().ToStruct().Get64(int(slot.Offset()))
				fprintf(w, "uint64(%d),", uint64(val))

			case TYPE_INT8:
				val := v.Struct().ToStruct().Get8(int(slot.Offset()))
				fprintf(w, "int8(%d),", int8(val))

			case TYPE_INT16:
				val := v.Struct().ToStruct().Get16(int(slot.Offset()))
				fprintf(w, "int16(%d),", int16(val))

			case TYPE_INT32:
				val := v.Struct().ToStruct().Get32(int(slot.Offset()))
				fprintf(w, "int32(%d),", int32(val))

			case TYPE_INT64:
				val := v.Struct().ToStruct().Get64(int(slot.Offset()))
				fprintf(w, "int64(%d),", int64(val))

			case TYPE_FLOAT32:
				val := v.Struct().ToStruct().Get32(int(slot.Offset()))
				fprintf(w, "float32(%f),", float32(val))

			case TYPE_FLOAT64:
				val := v.Struct().ToStruct().Get64(int(slot.Offset()))
				fprintf(w, "float64(%f),", float64(val))

			case TYPE_TEXT:
				val := v.Struct().ToStruct().GetObject(int(slot.Offset()))
				fprintf(w, "\"%s\",", val.ToText())

			default:
				panic("Unsupported value type")
			}
		}

		fprintf(w, "}\n")

	case TYPE_ANYPOINTER:
		fprintf(w, "interface{}")

	case TYPE_LIST:
		assert(v.Which() == VALUE_LIST, "expected list value")

		switch lt := t.List().ElementType(); lt.Which() {
		case TYPE_VOID:
			fprintf(w, "make([]C.Void, %d)", v.List().ToVoidList().Len())

		case TYPE_INTERFACE:
			fprintf(w, "make([]C.Interface, %d)", v.List().ToVoidList().Len())

		case TYPE_BOOL:
			fprintf(w, "[]bool{")
			for i, b := range v.List().ToBitList().ToArray() {
				if i > 0 {
					fprintf(w, ", ")
				}
				fprintf(w, "%v", b)
			}
			fprintf(w, "}")

		case TYPE_INT8:
			fprintf(w, "[]int8{")
			for i, b := range v.List().ToInt8List().ToArray() {
				if i > 0 {
					fprintf(w, ", ")
				}
				fprintf(w, "%d", b)
			}
			fprintf(w, "}")

		case TYPE_UINT8:
			fprintf(w, "[]uint8{")
			for i, b := range v.List().ToUInt8List().ToArray() {
				if i > 0 {
					fprintf(w, ", ")
				}
				fprintf(w, "%d", b)
			}
			fprintf(w, "}")

		case TYPE_INT16:
			fprintf(w, "[]int16{")
			for i, b := range v.List().ToInt16List().ToArray() {
				if i > 0 {
					fprintf(w, ", ")
				}
				fprintf(w, "%d", b)
			}
			fprintf(w, "}")

		case TYPE_UINT16:
			fprintf(w, "[]uint16{")
			for i, b := range v.List().ToUInt16List().ToArray() {
				if i > 0 {
					fprintf(w, ", ")
				}
				fprintf(w, "%d", b)
			}
			fprintf(w, "}")

		case TYPE_INT32:
			fprintf(w, "[]int32{")
			for i, b := range v.List().ToInt32List().ToArray() {
				if i > 0 {
					fprintf(w, ", ")
				}
				fprintf(w, "%d", b)
			}
			fprintf(w, "}")

		case TYPE_UINT32:
			fprintf(w, "[]uint32{")
			for i, b := range v.List().ToUInt32List().ToArray() {
				if i > 0 {
					fprintf(w, ", ")
				}
				fprintf(w, "%d", b)
			}
			fprintf(w, "}")

		case TYPE_FLOAT32:
			fprintf(w, "[]float32{")
			for i, b := range v.List().ToFloat32List().ToArray() {
				if i > 0 {
					fprintf(w, ", ")
				}
				fprintf(w, "%g", b)
			}
			fprintf(w, "}")

		case TYPE_INT64:
			fprintf(w, "[]int64{")
			for i, b := range v.List().ToInt64List().ToArray() {
				if i > 0 {
					fprintf(w, ", ")
				}
				fprintf(w, "%d", b)
			}
			fprintf(w, "}")

		case TYPE_UINT64:
			fprintf(w, "[]uint64{")
			for i, b := range v.List().ToUInt64List().ToArray() {
				if i > 0 {
					fprintf(w, ", ")
				}
				fprintf(w, "%d", b)
			}
			fprintf(w, "}")

		case TYPE_FLOAT64:
			fprintf(w, "[]float64{")
			for i, b := range v.List().ToFloat64List().ToArray() {
				if i > 0 {
					fprintf(w, ", ")
				}
				fprintf(w, "%g", b)
			}
			fprintf(w, "}")

		case TYPE_TEXT:
			fprintf(w, "[]string{")
			for i, b := range v.List().ToTextList().ToArray() {
				if i > 0 {
					fprintf(w, ", ")
				}
				fprintf(w, "\"%s\"", b)
			}
			fprintf(w, "}")

		case TYPE_DATA:
			fprintf(w, "[]byte{")
			for i, b := range v.List().ToDataList().ToArray() {
				if i > 0 {
					fprintf(w, ", ")
				}
				fprintf(w, "[]byte{")
				for i, ib := range b {
					if i > 0 {
						fprintf(w, ", ")
					}
					fprintf(w, "%d", ib)
				}
				fprintf(w, "}")
			}
			fprintf(w, "}")

		case TYPE_ENUM:
			en := findNode(lt.Enum().TypeId())
			fprintf(w, "[]%s{", en.remoteName(n))

			ev := en.Enum().Enumerants()
			a := v.List().ToUInt16List().ToEnumArray()

			for i, b := range *a {
				if i > 0 {
					fprintf(w, ", ")
				}

				fprintf(w, "%s", ev.At(int(b)).Name())
			}
			fprintf(w, "}")
		case TYPE_STRUCT:
			stype := findNode(lt.Struct().TypeId())
			fprintf(w, "[]%s{", stype.remoteName(n))
			fprintf(w, "/* Not implemented */")
			fprintf(w, "}")
		case TYPE_LIST, TYPE_ANYPOINTER:
			fprintf(w, "[]interface{")
			fprintf(w, "/* Not implemented */")
			fprintf(w, "}")
		}
	}
}

func (n *node) defineAnnotation(w io.Writer) {
	log.Println("Annotation", n.name)
	fprintf(w, "const %s = uint64(0x%x)\n", n.name, n.Id())
}

func constIsVar(n *node) bool {
	switch n.Const().Type().Which() {
	case TYPE_BOOL, TYPE_INT8, TYPE_UINT8, TYPE_INT16,
		TYPE_UINT16, TYPE_INT32, TYPE_UINT32, TYPE_INT64,
		TYPE_UINT64, TYPE_FLOAT32, TYPE_FLOAT64, TYPE_TEXT, TYPE_ENUM:
		return false
	default:
		return true
	}
}

func defineConstNodes(w io.Writer, nodes []*node) {
	any := false

	for _, n := range nodes {
		if n.Which() == NODE_CONST && !constIsVar(n) {
			if !any {
				fprintf(w, "const (\n")
				any = true
			}
			fprintf(w, "%s = ", n.name)
			n.writeValue(w, n.Const().Type(), n.Const().Value())
			fprintf(w, "\n")
		}
	}

	if any {
		fprintf(w, ")\n")
	}

	any = false

	for _, n := range nodes {
		if n.Which() == NODE_CONST && constIsVar(n) {
			if !any {
				fprintf(w, "var (\n")
				any = true
			}
			fprintf(w, "%s = ", n.name)
			n.writeValue(w, n.Const().Type(), n.Const().Value())
			fprintf(w, "\n")
		}
	}

	if any {
		fprintf(w, ")\n")
	}
}

func (n *node) defineField(w io.Writer, f Field, x *bam.Extractor) {
	t := f.Slot().Type()

	if t.Which() == TYPE_INTERFACE {
		return
	}

	fname := f.Name()

	if an := nameAnnotation(f.Annotations()); an != "" {
		fname = an
	}
	fname = title(fname)

	var g, s bytes.Buffer

	if f.DiscriminantValue() != 0xFFFF {
		if t.Which() == TYPE_VOID {
			fprintf(&s, "/* Not implemented */")
			w.Write(s.Bytes())
			return
		}
	} else if t.Which() == TYPE_VOID {
		return
	}

	customtype := ""
	for _, a := range f.Annotations().ToArray() {
		if a.Id() == C.Doc {
			fprintf(&g, "// %s\n", a.Value().Text())
		}
		if a.Id() == C.Customtype {
			customtype = a.Value().Text()
			if i := strings.LastIndex(customtype, "."); i != -1 {
				g_imported[customtype[:i]] = true
			}
		}
	}

	if len(customtype) != 0 {
		log.Println("CUSTOM TYPE:", customtype)
	}

	fprintf(&s, "%s ", fname)

	typeName := f.Slot().TypeName(n, customtype)
	fprintf(&s, "%s", typeName)

	fld := &ast.Field{}
	goseq := strings.SplitAfter(typeName, "[]")
	typePrefix := ""
	if len(goseq) == 2 {
		typeName = goseq[1]
		typePrefix = goseq[0]
	}

	x.GenerateStructField(fname, typePrefix, typeName, fld, t.Which() != TYPE_LIST, fld.Tag, false, goseq)

	if ans := f.Annotations(); ans.Len() != 0 {
		n.processAnnotations(&s, t.Which(), ans)
	}

	fprintf(&s, "\n")

	w.Write(g.Bytes())
	w.Write(s.Bytes())
}

func (s FieldSlot) TypeName(n *node, customtype string) string {
	def := s.DefaultValue()
	t := s.Type()

	switch t.Which() {
	case TYPE_BOOL:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_BOOL, "expected bool default")
		return "bool"

	case TYPE_INT8:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_INT8, "expected int8 default")
		return "int8"

	case TYPE_UINT8:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_UINT8, "expected uint8 default")
		return "uint8"

	case TYPE_INT16:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_INT16, "expected int16 default")
		return "int16"

	case TYPE_UINT16:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_UINT16, "expected uint16 default")
		return "uint16"

	case TYPE_INT32:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_INT32, "expected int32 default")
		return "int32"

	case TYPE_UINT32:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_UINT32, "expected uint32 default")
		return "uint32"

	case TYPE_INT64:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_INT64, "expected int64 default")
		return "int64"

	case TYPE_UINT64:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_UINT64, "expected uint64 default")
		return "uint64"

	case TYPE_FLOAT32:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_FLOAT32, "expected float32 default")
		return "float32"

	case TYPE_FLOAT64:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_FLOAT64, "expected float64 default")
		return "float64"

	case TYPE_TEXT:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_TEXT, "expected text default")

		return "string"

	case TYPE_DATA:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_DATA, "expected data default")
		if def.Which() == VALUE_DATA && len(def.Data()) > 0 {
			dstr := "[]byte{"
			for i, b := range def.Data() {
				if i > 0 {
					dstr += ", "
				}
				dstr += sprintf("%d", b)
			}
			dstr += "}"
			if len(customtype) != 0 {
				return sprintf("%s\n", dstr)
			}
		} else {
			return "[]byte"
		}
	case TYPE_ENUM:
		ni := findNode(t.Enum().TypeId())
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_ENUM, "expected enum default")
		return ni.remoteName(n)

	case TYPE_STRUCT:
		ni := findNode(t.Struct().TypeId())
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_STRUCT, "expected struct default")
		return ni.remoteName(n)

	case TYPE_ANYPOINTER:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_ANYPOINTER, "expected object default")
		return "interface{}"

	case TYPE_LIST:
		assert(def.Which() == VALUE_VOID || def.Which() == VALUE_LIST, "expected list default")

		switch lt := t.List().ElementType(); lt.Which() {
		case TYPE_VOID, TYPE_INTERFACE:
			return "[]struct{}"
		case TYPE_BOOL:
			return "[]bool"
		case TYPE_INT8:
			return "[]int8"
		case TYPE_UINT8:
			return "[]uint8"
		case TYPE_INT16:
			return "[]int16"
		case TYPE_UINT16:
			return "[]uint16"
		case TYPE_INT32:
			return "[]uint32"
		case TYPE_UINT32:
			return "[]uint32"
		case TYPE_INT64:
			return "[]int64"
		case TYPE_UINT64:
			return "[]uint64"
		case TYPE_FLOAT32:
			return "[]float32"
		case TYPE_FLOAT64:
			return "[]float64"
		case TYPE_TEXT:
			return "[]string"
		case TYPE_DATA:
			return "[]byte"
		case TYPE_ENUM:
			ni := findNode(lt.Enum().TypeId())
			return sprintf("%s", ni.remoteName(n))
		case TYPE_STRUCT:
			ni := findNode(lt.Struct().TypeId())

			return sprintf("[]%s", ni.name)
		case TYPE_ANYPOINTER, TYPE_LIST:
			return "[]interface{}"
		}
	}

	panic("Unsupported type. Type_Which=" + strconv.Itoa(int(t.Which())))
}

func (n *node) codeOrderFields() []Field {
	fields := n.Struct().Fields().ToArray()
	mbrs := make([]Field, len(fields))
	for _, f := range fields {
		mbrs[f.CodeOrder()] = f
	}
	return mbrs
}

func (n *node) defineStructTypes(w io.Writer, baseNode *node, x *bam.Extractor) {
	assert(n.Which() == NODE_STRUCT, "invalid struct node")

	for _, a := range n.Annotations().ToArray() {
		if a.Id() == C.Doc {
			fprintf(w, "// %s\n", a.Value().Text())
		}
	}
	if baseNode == nil {
		x.StartStruct(n.name)

		fprintf(w, "type %s struct {\n", n.name)
		n.defineStructFields(w, x)
		fprintf(w, "}\n\n")

		baseNode = n
		x.EndStruct()
	}

	for _, f := range n.codeOrderFields() {
		if f.Which() == FIELD_GROUP {
			findNode(f.Group().TypeId()).defineStructTypes(w, baseNode, x)
		}
	}
}

func (n *node) defineStructEnums(w io.Writer) {
	assert(n.Which() == NODE_STRUCT, "invalid struct node")

	if n.Struct().DiscriminantCount() > 0 {
		fprintf(w, "type %s_Which uint16\n", n.name)
		fprintf(w, "const (\n")

		for _, f := range n.codeOrderFields() {
			if f.DiscriminantValue() == 0xFFFF {
				// Non-union member
			} else {
				fprintf(w, "%s_%s %s_Which = %d\n", strings.ToUpper(n.name), strings.ToUpper(f.Name()), n.name, f.DiscriminantValue())
			}
		}
		fprintf(w, ")\n")
	}

	for _, f := range n.codeOrderFields() {
		if f.Which() == FIELD_GROUP {
			findNode(f.Group().TypeId()).defineStructEnums(w)
		}
	}
}

func (n *node) defineStructFields(w io.Writer, x *bam.Extractor) {
	assert(n.Which() == NODE_STRUCT, "invalid struct node")

	for _, f := range n.codeOrderFields() {
		switch f.Which() {
		case FIELD_SLOT:
			n.defineField(w, f, x)
		case FIELD_GROUP:
			g := findNode(f.Group().TypeId())
			fname := f.Name()
			if an := nameAnnotation(f.Annotations()); an != "" {
				fname = an
			}
			fname = title(fname)

			typeName := ""
			fld := &ast.Field{}
			x.GenerateStructField(fname, "", typeName, fld, false, fld.Tag, true, []string{typeName})

			fprintf(w, "%s struct {\n", fname)
			g.defineStructFields(w, x)

			fprintf(w, "}\n")
		}
	}
}

// This writes the WriteJSON function.
//
// This is an unusual interface, but it was chosen because the types in go-capnproto
// didn't match right to use the json.Marshaler interface.
// This function recurses through the type, writing statements that will dump json to a wire
// For all statements, the json encoder js and the bufio writer b will be in scope.
// The value will be in scope as s. Some features need to redefine s, like unions.
// In that case, Make a new block and redeclare s
func (n *node) defineTypeJsonFuncs(w io.Writer) {
	if C.JSON_enabled {
		g_imported["io"] = true
		g_imported["bufio"] = true
		g_imported["bytes"] = true

		fprintf(w, "func (s %s) WriteJSON(w io.Writer) error {\n", n.name)
		fprintf(w, "b := bufio.NewWriter(w);")
		fprintf(w, "var err error;")
		fprintf(w, "var buf []byte;")
		fprintf(w, "_ = buf;")

		switch n.Which() {
		case NODE_ENUM:
			n.jsonEnum(w)
		case NODE_STRUCT:
			n.jsonStruct(w)
		}

		fprintf(w, "err = b.Flush(); return err\n};\n")

		fprintf(w, "func (s %s) MarshalJSON() ([]byte, error) {\n", n.name)
		fprintf(w, "b := bytes.Buffer{}; err := s.WriteJSON(&b); return b.Bytes(), err };")

	} else {
		fprintf(w, "// capn.JSON_enabled == false so we stub MarshallJSON().")
		fprintf(w, "\nfunc (s %s) MarshalJSON() (bs []byte, err error) { return } \n", n.name)
	}
}

func writeErrCheck(w io.Writer) {
	fprintf(w, "if err != nil { return err; };")
}

func (n *node) jsonEnum(w io.Writer) {
	g_imported["encoding/json"] = true
	fprintf(w, `buf, err = json.Marshal(s.String());`)
	writeErrCheck(w)
	fprintf(w, "_, err = b.Write(buf);")
	writeErrCheck(w)
}

// Write statements that will write a json struct
func (n *node) jsonStruct(w io.Writer) {
	fprintf(w, `err = b.WriteByte('{');`)
	writeErrCheck(w)
	for i, f := range n.codeOrderFields() {
		if f.DiscriminantValue() != 0xFFFF {
			enumname := fmt.Sprintf("%s_%s", strings.ToUpper(n.name), strings.ToUpper(f.Name()))
			fprintf(w, "if s.Which() == %s {", enumname)
		} else if i != 0 {
			fprintf(w, `
				err = b.WriteByte(',');
			`)
			writeErrCheck(w)
		}
		fprintf(w, `_, err = b.WriteString("\"%s\":");`, f.Name())
		writeErrCheck(w)
		f.json(w)
		if f.DiscriminantValue() != 0xFFFF {
			fprintf(w, "};")
		}
	}
	fprintf(w, `err = b.WriteByte('}');`)
	writeErrCheck(w)
}

// This function writes statements that write the fields json representation to the bufio.
func (f *Field) json(w io.Writer) {
	switch f.Which() {
	case FIELD_SLOT:
		fs := f.Slot()
		// we don't generate setters for Void fields
		if fs.Type().Which() == TYPE_VOID {
			fs.Type().json(w)
			return
		}
		fprintf(w, "{ s := s.%s(); ", title(f.Name()))
		fs.Type().json(w)
		fprintf(w, "}; ")
	case FIELD_GROUP:
		tid := f.Group().TypeId()
		n := findNode(tid)
		fprintf(w, "{ s := s.%s();", title(f.Name()))

		n.jsonStruct(w)
		fprintf(w, "};")
	}
}

func (t Type) json(w io.Writer) {
	switch t.Which() {
	case TYPE_UINT8, TYPE_UINT16, TYPE_UINT32, TYPE_UINT64,
		TYPE_INT8, TYPE_INT16, TYPE_INT32, TYPE_INT64,
		TYPE_FLOAT32, TYPE_FLOAT64, TYPE_BOOL, TYPE_TEXT, TYPE_DATA:
		g_imported["encoding/json"] = true
		fprintf(w, "buf, err = json.Marshal(s);")
		writeErrCheck(w)
		fprintf(w, "_, err = b.Write(buf);")
		writeErrCheck(w)
	case TYPE_ENUM, TYPE_STRUCT:
		// since we handle groups at the field level, only named struct types make it in here
		// so we can just call the named structs json dumper
		fprintf(w, "err = s.WriteJSON(b);")
		writeErrCheck(w)
	case TYPE_LIST:
		typ := t.List().ElementType()
		which := typ.Which()
		if which == TYPE_LIST || which == TYPE_ANYPOINTER {
			// untyped list, cant do anything but report
			// that a field existed.
			//
			// s will be unused in this case, so ignore
			fprintf(w, `_ = s;`)
			fprintf(w, `_, err = b.WriteString("\"untyped list\"");`)
			writeErrCheck(w)
			return
		}
		fprintf(w, "{ err = b.WriteByte('[');")
		writeErrCheck(w)
		fprintf(w, "for i, s := range s.ToArray() {")
		fprintf(w, `if i != 0 { _, err = b.WriteString(", "); };`)
		writeErrCheck(w)
		typ.json(w)
		fprintf(w, "}; err = b.WriteByte(']'); };")
		writeErrCheck(w)
	case TYPE_VOID:
		fprintf(w, `_ = s;`)
		fprintf(w, `_, err = b.WriteString("null");`)
		writeErrCheck(w)
	}
}

func (n *node) writeImports(file *os.File) {
	if n.imp != "" || len(g_imported) > 0 {
		fprintf(file, "import (\n")
		if n.imp != "" {
			fprintf(file, "    %q\n", n.imp)
		}

		for imp := range g_imported {
			fprintf(file, "    %q\n", imp)
		}

		fprintf(file, ")\n\n")
	}
}

func (n *node) processAnnotations(w io.Writer, t Type_Which, ans Annotation_List) {
	set := make(map[uint64]bool)

	for _, a := range ans.ToArray() {
		set[a.Id()] = true
	}

	assert((set[caps.Required] && set[caps.Ignored]) != true, "Field annnotations 'required' and 'ignored' are incompatible.")
	assert((set[caps.Required] && set[caps.Optional]) != true, "Annnotations 'required' and 'optional' are incompatible")
	assert((set[caps.Optional] && set[caps.Ignored]) != true, "Annnotations 'optional' and 'ignored' are incompatible")

	tags := []string{}

	for _, a := range ans.ToArray() {
		// Codecs Tags
		switch a.Id() {
		case caps.Ignored:
			if _, found := n.codecs[caps.Json]; found {
				tags = append(tags, fmt.Sprintf("json:\"-\""))
			}
			if _, found := n.codecs[caps.Msgp]; found {
				tags = append(tags, fmt.Sprintf("msg:\"-\""))
			}
		case caps.Optional:
			if _, found := n.codecs[caps.Json]; found {
				tags = append(tags, fmt.Sprintf("json:\"%s,omitempty\"", a.Value().Text()))
			}
			if _, found := n.codecs[caps.Msgp]; found {
				tags = append(tags, fmt.Sprintf("msg:\"%s\"", a.Value().Text()))
			}
		case caps.Required:
			if _, found := n.codecs[caps.Json]; found {
				tags = append(tags, fmt.Sprintf("json:\"%s\"", a.Value().Text()))
			}
			if _, found := n.codecs[caps.Msgp]; found {
				tags = append(tags, fmt.Sprintf("msg:\"%s\"", a.Value().Text()))
			}
		}

		// Check Tags
		switch t {
		case TYPE_INT8, TYPE_UINT8, TYPE_INT16, TYPE_UINT16, TYPE_INT32,
			TYPE_UINT32, TYPE_INT64, TYPE_UINT64, TYPE_FLOAT32, TYPE_FLOAT64:
			switch a.Id() {
			case caps.Multof:
				tags = append(tags, fmt.Sprintf("multof:\"%s\"", a.Value().Int32()))
			case caps.Min:
				tags = append(tags, fmt.Sprintf("min:\"%s\"", a.Value().Int64()))
			case caps.Max:
				tags = append(tags, fmt.Sprintf("max:\"%d\"", a.Value().Int64()))
			}

		case TYPE_TEXT:
			switch a.Id() {
			case caps.Format:
				tags = append(tags, fmt.Sprintf("format:\"%s\"", a.Value().Text()))
			case caps.Pattern:
				tags = append(tags, fmt.Sprintf("pattern:\"%s\"", a.Value().Text()))
			case caps.Minlen:
				tags = append(tags, fmt.Sprintf("minlen:\"%d\"", a.Value().Int32()))
			case caps.Maxlen:
				tags = append(tags, fmt.Sprintf("maxlen:\"%d\"", a.Value().Int32()))
			}

		case TYPE_LIST:
			switch a.Id() {
			case caps.Unique:
				tags = append(tags, fmt.Sprintf("unique:\"true\""))
			case caps.Minlen:
				tags = append(tags, fmt.Sprintf("minlen:\"%d\"", a.Value().Int32()))
			case caps.Maxlen:
				tags = append(tags, fmt.Sprintf("maxlen:\"%d\"", a.Value().Int32()))
			}
		}

		// if i != ans.Len()-1 {
		// 	tags = append(tags, fprintf(" "))
		// } else {
		// 	fprintf(w, "`")
		// }
	}

	if len(tags) != 0 {
		fprintf(w, "`%s`", strings.Join(tags, " "))
	}
}

func main() {
	s, err := C.ReadFromStream(os.Stdin, nil)
	assert(err == nil, "%v\n", err)

	req := ReadRootCodeGeneratorRequest(s)
	allfiles := []*node{}

	for _, ni := range req.Nodes().ToArray() {
		n := &node{Node: ni, codecs: make(map[uint64]bool)}
		g_nodes[n.Id()] = n

		if n.Which() == NODE_FILE {
			allfiles = append(allfiles, n)
		}
	}

	g_imported = make(map[string]bool)

	for _, f := range allfiles {
		for _, a := range f.Annotations().ToArray() {
			if v := a.Value(); v.Which() == VALUE_TEXT {
				switch a.Id() {
				case C.Package:
					f.pkg = v.Text()
				case C.Import:
					f.imp = v.Text()
				}
			} else {
				switch a.Id() {
				case caps.Capnp:
					enableCodec(f, caps.Capnp)
					g_imported["io"] = true
					g_imported[GO_CAPNP_IMPORT] = true
				case caps.Json:
					enableCodec(f, caps.Json)
				case caps.Msgp:
					enableCodec(f, caps.Msgp)
				}
			}
		}

		for _, nn := range f.NestedNodes().ToArray() {
			if ni := g_nodes[nn.Id()]; ni != nil {
				ni.resolveName("", nn.Name(), f)
			}
		}
	}

	for _, reqf := range req.RequestedFiles().ToArray() {
		x := bam.NewExtractor()
		x.FieldPrefix = "   "
		x.FieldSuffix = "\n"

		f := findNode(reqf.Id())
		buf := bytes.Buffer{}
		g_segment = C.NewBuffer([]byte{})

		for _, n := range f.nodes {
			if n.Which() == NODE_ANNOTATION {
				n.defineAnnotation(&buf)
			}
		}

		defineConstNodes(&buf, f.nodes)

		for _, n := range f.nodes {
			switch n.Which() {
			case NODE_ANNOTATION:
				log.Println("Node annotation:", n)
				// n.defineAnnotation(&buf)
			case NODE_ENUM:
				n.defineEnum(&buf)
			case NODE_STRUCT:
				if !n.Struct().IsGroup() {
					n.defineStructTypes(&buf, nil, x)
					n.defineStructEnums(&buf)
				}
			}
		}

		// Write translation functions
		if _, found := f.codecs[caps.Capnp]; found {
			log.Println("Writing translator functions")
			_, err = x.WriteToTranslators(&buf)
			assert(err == nil, "%v\n", err)
		}

		assert(f.pkg != "", "missing package annotation for %s", reqf.Filename())
		x.PkgName = f.pkg

		if dirPath, _ := filepath.Split(reqf.Filename()); dirPath != "" {
			err := os.MkdirAll(dirPath, os.ModePerm)
			assert(err == nil, "%v\n", err)
			x.OutDir = dirPath
		}

		// Create output file
		filename := strings.TrimSuffix(reqf.Filename(), ".capnp")

		file, err := os.Create(filename + ".go")
		assert(err == nil, "%v\n", err)

		// Write package
		fprintf(file, "package %s\n\n", f.pkg)
		fprintf(file, "// AUTO GENERATED - DO NOT EDIT\n\n")

		// Write imports
		f.writeImports(file)

		// Format sources
		clean, err := format.Source(buf.Bytes())
		assert(err == nil, "%v\n", err)
		file.Write(clean)

		defer file.Close()
	}
}

func enableCodec(n *node, codec uint64) {
	n.codecs[codec] = true
	for _, nst := range n.NestedNodes().ToArray() {
		nn := findNode(nst.Id())
		enableCodec(nn, codec)
	}
}
