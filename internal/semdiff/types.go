package semdiff

import (
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// typeCompat reports whether every value the old type constraint accepts is
// still accepted by the new one. Unparseable (pre-0.12) types count as
// incompatible: over-warning beats a silent break.
func typeCompat(oldT, newT string) bool {
	o, ok1 := parseType(oldT)
	n, ok2 := parseType(newT)
	return ok1 && ok2 && accepts(o, n)
}

func parseType(t string) (cty.Type, bool) {
	t = strings.Trim(t, `"`) // Terraform 0.11 allowed quoted, bare type keywords
	switch t {
	case "":
		return cty.DynamicPseudoType, true
	case "list":
		t = "list(any)"
	case "map":
		t = "map(any)"
	}
	expr, diags := hclsyntax.ParseExpression([]byte(t), "", hcl.InitialPos)
	if diags.HasErrors() {
		return cty.NilType, false
	}
	ty, _, diags := typeexpr.TypeConstraintWithDefaults(expr)
	return ty, !diags.HasErrors()
}

// accepts: can every value of type o be passed where n is expected?
func accepts(o, n cty.Type) bool {
	switch {
	case n == cty.DynamicPseudoType:
		return true
	case o == cty.DynamicPseudoType:
		return false // narrowing from any
	case o.IsPrimitiveType() && n.IsPrimitiveType():
		// Terraform converts numbers and bools to strings automatically.
		return o.Equals(n) || (n == cty.String && (o == cty.Number || o == cty.Bool))
	case o.IsListType() && n.IsListType(),
		o.IsSetType() && n.IsSetType(),
		o.IsMapType() && n.IsMapType():
		return accepts(o.ElementType(), n.ElementType())
	case o.IsTupleType() && n.IsTupleType():
		oe, ne := o.TupleElementTypes(), n.TupleElementTypes()
		if len(oe) != len(ne) {
			return false
		}
		for i := range oe {
			if !accepts(oe[i], ne[i]) {
				return false
			}
		}
		return true
	case o.IsObjectType() && n.IsObjectType():
		oa, na := o.AttributeTypes(), n.AttributeTypes()
		for name, ot := range oa {
			nt, ok := na[name]
			if !ok || !accepts(ot, nt) {
				return false // a dropped attribute silently discards caller input
			}
			if o.AttributeOptional(name) && !n.AttributeOptional(name) {
				return false // became required inside the object
			}
		}
		for name := range na {
			if _, ok := oa[name]; !ok && !n.AttributeOptional(name) {
				return false // new required attribute
			}
		}
		return true
	default:
		return false
	}
}
