// modeled on server/internal/vprovider/kvm/live_libvirt.go (see CASTOR-REUSE.md)
//
// live_xmlrpc.go is a tiny, dependency-free XML-RPC codec for the XAPI wire protocol
// (stdlib encoding/xml only). It encodes <methodCall> requests and decodes
// <methodResponse> bodies into a generic xmlrpcValue tree, then unwraps the XAPI
// envelope ({"Status": "Success"|"Failure", "Value"|"ErrorDescription": ...}). This
// is the REAL XAPI client wire format spoken by XenServer / XCP-ng; the live_xapi
// tests drive it against recorded REAL XAPI responses.
package xen

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// xmlrpcValue is a decoded XML-RPC <value> (string|int|boolean|double|struct|array).
// It is also used to build request params.
type xmlrpcValue struct {
	kind   xmlrpcKind
	str    string
	fields map[string]*xmlrpcValue // struct
	items  []*xmlrpcValue          // array
}

type xmlrpcKind int

const (
	kindString xmlrpcKind = iota
	kindInt
	kindBool
	kindDouble
	kindStruct
	kindArray
)

// --- value constructors (for request params) ---

func xmlrpcString(s string) xmlrpcValue { return xmlrpcValue{kind: kindString, str: s} }
func xmlrpcBool(v bool) xmlrpcValue {
	s := "0"
	if v {
		s = "1"
	}
	return xmlrpcValue{kind: kindBool, str: s}
}
func xmlrpcStruct(m map[string]xmlrpcValue) xmlrpcValue {
	fields := make(map[string]*xmlrpcValue, len(m))
	for k, v := range m {
		vc := v
		fields[k] = &vc
	}
	return xmlrpcValue{kind: kindStruct, fields: fields}
}

// --- accessors (for decoded responses) ---

// text returns the scalar string form of a value ("" for non-scalars).
func (v *xmlrpcValue) text() string {
	if v == nil {
		return ""
	}
	switch v.kind {
	case kindString, kindInt, kindBool, kindDouble:
		return v.str
	}
	return ""
}

// structMap returns a struct value's fields as ref->record(value). XAPI
// *.get_all_records returns a struct keyed by opaque ref, each value a record struct.
func (v *xmlrpcValue) structMap() map[string]*xmlrpcValue {
	if v == nil || v.kind != kindStruct {
		return nil
	}
	return v.fields
}

// structMapFlat flattens a record struct to a string map (scalar fields only). Used
// for one-off record parsing (e.g. snapshot detail).
func (v *xmlrpcValue) structMapFlat() map[string]string {
	out := map[string]string{}
	if v == nil || v.kind != kindStruct {
		return out
	}
	for k, fv := range v.fields {
		out[k] = fv.text()
	}
	return out
}

// field returns a named struct field (nil if absent / not a struct).
func (v *xmlrpcValue) field(name string) *xmlrpcValue {
	if v == nil || v.kind != kindStruct {
		return nil
	}
	return v.fields[name]
}

// arrayValues returns an array value's items.
func (v *xmlrpcValue) arrayValues() []*xmlrpcValue {
	if v == nil || v.kind != kindArray {
		return nil
	}
	return v.items
}

// --- request encoding ---

func encodeMethodCall(method string, params []xmlrpcValue) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0"?>`)
	b.WriteString("<methodCall><methodName>")
	if err := xml.EscapeText(&b, []byte(method)); err != nil {
		return nil, err
	}
	b.WriteString("</methodName><params>")
	for i := range params {
		b.WriteString("<param>")
		if err := encodeValue(&b, &params[i]); err != nil {
			return nil, err
		}
		b.WriteString("</param>")
	}
	b.WriteString("</params></methodCall>")
	return b.Bytes(), nil
}

func encodeValue(b *bytes.Buffer, v *xmlrpcValue) error {
	b.WriteString("<value>")
	switch v.kind {
	case kindString:
		b.WriteString("<string>")
		if err := xml.EscapeText(b, []byte(v.str)); err != nil {
			return err
		}
		b.WriteString("</string>")
	case kindBool:
		b.WriteString("<boolean>" + v.str + "</boolean>")
	case kindInt:
		b.WriteString("<int>" + v.str + "</int>")
	case kindDouble:
		b.WriteString("<double>" + v.str + "</double>")
	case kindStruct:
		b.WriteString("<struct>")
		for name, fv := range v.fields {
			b.WriteString("<member><name>")
			if err := xml.EscapeText(b, []byte(name)); err != nil {
				return err
			}
			b.WriteString("</name>")
			if err := encodeValue(b, fv); err != nil {
				return err
			}
			b.WriteString("</member>")
		}
		b.WriteString("</struct>")
	case kindArray:
		b.WriteString("<array><data>")
		for _, iv := range v.items {
			if err := encodeValue(b, iv); err != nil {
				return err
			}
		}
		b.WriteString("</data></array>")
	}
	b.WriteString("</value>")
	return nil
}

// --- response decoding ---

// xMethodResponse mirrors an XML-RPC <methodResponse> for decoding.
type xMethodResponse struct {
	XMLName xml.Name  `xml:"methodResponse"`
	Params  []xParam  `xml:"params>param"`
	Fault   *xRawVal  `xml:"fault>value"`
}

type xParam struct {
	Value xRawVal `xml:"value"`
}

// xRawVal is a raw XML-RPC <value> decoded into typed sub-elements. Exactly one of
// the scalar/struct/array fields is populated. CharData captures the untyped-string
// case (<value>text</value> with no type tag, valid in XML-RPC).
type xRawVal struct {
	Str      *string     `xml:"string"`
	Int      *string     `xml:"int"`
	I4       *string     `xml:"i4"`
	Bool     *string     `xml:"boolean"`
	Double   *string     `xml:"double"`
	Struct   *xStruct    `xml:"struct"`
	Array    *xArray     `xml:"array"`
	CharData string      `xml:",chardata"`
}

type xStruct struct {
	Members []xMember `xml:"member"`
}

type xMember struct {
	Name  string  `xml:"name"`
	Value xRawVal `xml:"value"`
}

type xArray struct {
	Values []xRawVal `xml:"data>value"`
}

// decodeMethodResponse parses a <methodResponse> body and unwraps the XAPI envelope.
func decodeMethodResponse(raw []byte) (*xmlrpcValue, error) {
	var mr xMethodResponse
	if err := xml.Unmarshal(raw, &mr); err != nil {
		return nil, fmt.Errorf("xen: decode XML-RPC response: %w", err)
	}
	if mr.Fault != nil {
		// XML-RPC transport-level fault (e.g. malformed call).
		fv := convertRawVal(mr.Fault)
		code := fv.field("faultString").text()
		if code == "" {
			code = "XMLRPC_FAULT"
		}
		return nil, &xapiFault{code: code}
	}
	if len(mr.Params) == 0 {
		return nil, fmt.Errorf("xen: XML-RPC response has no params")
	}
	top := convertRawVal(&mr.Params[0].Value)
	// XAPI wraps the real result in a struct {"Status": ..., "Value"/"ErrorDescription"}.
	status := top.field("Status")
	if status == nil {
		// Not an XAPI envelope (rare) — return the value verbatim.
		return top, nil
	}
	if status.text() == "Success" {
		if val := top.field("Value"); val != nil {
			return val, nil
		}
		return &xmlrpcValue{kind: kindString}, nil
	}
	// Failure: ErrorDescription is an array whose [0] is the XAPI error code.
	ed := top.field("ErrorDescription")
	f := &xapiFault{}
	for i, item := range ed.arrayValues() {
		if i == 0 {
			f.code = item.text()
		}
		f.params = append(f.params, item.text())
	}
	if f.code == "" {
		f.code = "UNKNOWN"
	}
	return nil, f
}

// convertRawVal turns a decoded xRawVal into the generic xmlrpcValue tree.
func convertRawVal(r *xRawVal) *xmlrpcValue {
	switch {
	case r == nil:
		return &xmlrpcValue{kind: kindString}
	case r.Str != nil:
		return &xmlrpcValue{kind: kindString, str: *r.Str}
	case r.Int != nil:
		return &xmlrpcValue{kind: kindInt, str: strings.TrimSpace(*r.Int)}
	case r.I4 != nil:
		return &xmlrpcValue{kind: kindInt, str: strings.TrimSpace(*r.I4)}
	case r.Bool != nil:
		return &xmlrpcValue{kind: kindBool, str: strings.TrimSpace(*r.Bool)}
	case r.Double != nil:
		return &xmlrpcValue{kind: kindDouble, str: strings.TrimSpace(*r.Double)}
	case r.Struct != nil:
		fields := make(map[string]*xmlrpcValue, len(r.Struct.Members))
		for i := range r.Struct.Members {
			m := &r.Struct.Members[i]
			fields[m.Name] = convertRawVal(&m.Value)
		}
		return &xmlrpcValue{kind: kindStruct, fields: fields}
	case r.Array != nil:
		items := make([]*xmlrpcValue, 0, len(r.Array.Values))
		for i := range r.Array.Values {
			items = append(items, convertRawVal(&r.Array.Values[i]))
		}
		return &xmlrpcValue{kind: kindArray, items: items}
	default:
		// untyped <value>text</value>
		return &xmlrpcValue{kind: kindString, str: strings.TrimSpace(r.CharData)}
	}
}

// --- XAPI fault -> contract sentinel mapping ---

// xapiFault is a logical XAPI Failure carrying the error code + params.
type xapiFault struct {
	code   string
	params []string
}

func (e *xapiFault) Error() string {
	if len(e.params) > 1 {
		return "xapi: " + e.code + ": " + strings.Join(e.params[1:], ", ")
	}
	return "xapi: " + e.code
}

// mapXapiErr maps an XAPI error code to the contract sentinel. Codes per the XenAPI
// error reference (xapi/ocaml/xapi-consts/api_errors.ml).
func mapXapiErr(err error) error {
	f, ok := err.(*xapiFault)
	if !ok {
		return err
	}
	switch f.code {
	case "HANDLE_INVALID", "UUID_INVALID", "VM_SNAPSHOT_WITH_QUIESCE_NOT_FOUND",
		"HOST_OFFLINE":
		return vp.ErrNotFound
	case "VM_BAD_POWER_STATE", "OPERATION_NOT_ALLOWED", "OTHER_OPERATION_IN_PROGRESS",
		"VM_IS_TEMPLATE", "HOST_NOT_ENOUGH_FREE_MEMORY", "VM_REQUIRES_SR",
		"OBJECT_NOLONGER_EXISTS", "VM_MISSING_PV_DRIVERS":
		return vp.ErrConflict
	case "INVALID_VALUE", "FIELD_TYPE_ERROR", "VALUE_NOT_SUPPORTED",
		"MESSAGE_PARAMETER_COUNT_MISMATCH", "INVALID_DEVICE":
		return vp.ErrInvalidSpec
	default:
		return err
	}
}

// parseXapiTime parses an XAPI dateTime.iso8601 ("20240115T13:45:00Z" or RFC3339).
func parseXapiTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{"20060102T15:04:05Z", "20060102T15:04:05", time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func atoiSafe(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atoi64Safe(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func xapiBool(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	return s == "1" || s == "true"
}
